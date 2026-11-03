package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// dbtx is what both the pool and a transaction can do, so the same pricing
// code runs inside CreateOrder's transaction and in the cart preview.
type dbtx interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type PromoHandler struct {
	DB *pgxpool.Pool
}

type PromoCode struct {
	ID          int     `json:"id"`
	Code        string  `json:"code"`
	Kind        string  `json:"kind"` // percent | fixed
	Value       float64 `json:"value"`
	MinTotal    float64 `json:"min_total"`
	MaxUses     *int    `json:"max_uses"` // nil = no limit
	OncePerUser bool    `json:"once_per_user"`
	ValidUntil  *string `json:"valid_until"` // YYYY-MM-DD, inclusive; nil = no end
	IsActive    bool    `json:"is_active"`
	Description string  `json:"description"`
	Uses        int     `json:"uses"`       // orders that used it (cancelled ones do not count)
	Discounted  float64 `json:"discounted"` // ₸ given away by these orders
}

var promoCodePattern = regexp.MustCompile(`^[A-Z0-9_-]{3,32}$`)

// normalizePromo lets guests type the code in any case and with stray spaces.
func normalizePromo(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// promoError is a reason the code cannot be applied to this order.
// The frontend turns Code into a sentence for the guest.
type promoError struct {
	Code     string
	MinTotal float64
}

func (e *promoError) Error() string { return e.Code }

func (e *promoError) respond(c *gin.Context) {
	body := gin.H{"error": e.Code}
	if e.Code == "promo_min_total" {
		body["min_total"] = e.MinTotal
	}
	c.JSON(http.StatusBadRequest, body)
}

// applyPromo checks the code for this user and order and returns the discount.
// With lock = true (inside CreateOrder's transaction) the promo row is locked
// FOR UPDATE, so two simultaneous orders cannot both take the last use.
// Uses are counted from orders, so cancelling an order gives the use back.
func applyPromo(ctx context.Context, q dbtx, userID int, rawCode string, subtotal float64, lock bool) (*PromoCode, float64, error) {
	code := normalizePromo(rawCode)
	if !promoCodePattern.MatchString(code) {
		return nil, 0, &promoError{Code: "promo_not_found"}
	}

	query := `SELECT id, code, kind, value, min_total, max_uses, once_per_user,
	                 to_char(valid_until, 'YYYY-MM-DD'), is_active, description,
	                 valid_until IS NOT NULL AND valid_until < CURRENT_DATE
	          FROM promo_codes WHERE code = $1`
	if lock {
		query += " FOR UPDATE"
	}
	var p PromoCode
	var expired bool
	err := q.QueryRow(ctx, query, code).Scan(&p.ID, &p.Code, &p.Kind, &p.Value, &p.MinTotal, &p.MaxUses,
		&p.OncePerUser, &p.ValidUntil, &p.IsActive, &p.Description, &expired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, &promoError{Code: "promo_not_found"}
	}
	if err != nil {
		return nil, 0, err
	}

	switch {
	case !p.IsActive:
		return nil, 0, &promoError{Code: "promo_inactive"}
	case expired:
		return nil, 0, &promoError{Code: "promo_expired"}
	case subtotal < p.MinTotal:
		return nil, 0, &promoError{Code: "promo_min_total", MinTotal: p.MinTotal}
	}

	var uses int
	var usedByMe bool
	err = q.QueryRow(ctx,
		`SELECT count(*), COALESCE(bool_or(user_id = $2), false)
		 FROM orders WHERE promo_code_id = $1 AND status <> 'cancelled'`,
		p.ID, userID).Scan(&uses, &usedByMe)
	if err != nil {
		return nil, 0, err
	}
	if p.MaxUses != nil && uses >= *p.MaxUses {
		return nil, 0, &promoError{Code: "promo_exhausted"}
	}
	if p.OncePerUser && usedByMe {
		return nil, 0, &promoError{Code: "promo_used"}
	}
	p.Uses = uses

	return &p, promoDiscount(p.Kind, p.Value, subtotal), nil
}

// promoDiscount is never more than the order itself. Percent discounts are
// rounded down to whole tenge, in the restaurant's favour.
func promoDiscount(kind string, value, subtotal float64) float64 {
	discount := value
	if kind == "percent" {
		discount = math.Floor(subtotal * value / 100)
	}
	return math.Min(discount, subtotal)
}

// ---------- Cart preview ----------

type CheckPromoRequest struct {
	Code  string      `json:"code" binding:"required"`
	Items []OrderLine `json:"items" binding:"required,min=1,max=30,dive"`
}

// CheckPromo — POST /api/promo/check
// Shows the discount in the cart before the order is placed. The order itself
// checks the code again: the cart may change, and the last use may be taken.
func (h *PromoHandler) CheckPromo(c *gin.Context) {
	userID, _ := currentUser(c)

	var req CheckPromoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "code and items are required"})
		return
	}

	priced, err := priceItems(c, h.DB, req.Items)
	var unavailable *dishUnavailableError
	if errors.As(err, &unavailable) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "dish is not available", "dish_id": unavailable.DishID})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check promo code"})
		return
	}

	promo, discount, err := applyPromo(c, h.DB, userID, req.Code, priced.subtotal, false)
	var reason *promoError
	if errors.As(err, &reason) {
		reason.respond(c)
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to check promo code"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":        promo.Code,
		"kind":        promo.Kind,
		"value":       promo.Value,
		"description": promo.Description,
		"subtotal":    priced.subtotal,
		"discount":    discount,
		"total":       priced.subtotal - discount,
	})
}

// ---------- Admin ----------

type PromoRequest struct {
	Code        string  `json:"code"`
	Kind        string  `json:"kind"`
	Value       float64 `json:"value"`
	MinTotal    float64 `json:"min_total"`
	MaxUses     *int    `json:"max_uses"`
	OncePerUser *bool   `json:"once_per_user"` // default true
	ValidUntil  string  `json:"valid_until"`   // "" = no end
	IsActive    *bool   `json:"is_active"`     // default true
	Description string  `json:"description"`
}

// validate normalizes the request and returns an error text for the API, or "".
func (r *PromoRequest) validate() string {
	r.Code = normalizePromo(r.Code)
	r.Description = strings.TrimSpace(r.Description)
	r.ValidUntil = strings.TrimSpace(r.ValidUntil)

	switch {
	case !promoCodePattern.MatchString(r.Code):
		return "code must be 3-32 latin letters, digits, - or _"
	case r.Kind != "percent" && r.Kind != "fixed":
		return "kind must be percent or fixed"
	case r.Kind == "percent" && (r.Value < 1 || r.Value > 90):
		return "percent discount must be 1-90"
	case r.Kind == "fixed" && (r.Value < 1 || r.Value > 1_000_000):
		return "fixed discount must be 1-1000000"
	case r.MinTotal < 0 || r.MinTotal > 10_000_000:
		return "min_total must be 0-10000000"
	case r.MaxUses != nil && (*r.MaxUses < 1 || *r.MaxUses > 1_000_000):
		return "max_uses must be 1-1000000 or empty"
	case utf8.RuneCountInString(r.Description) > 200:
		return "description is too long"
	}
	if r.ValidUntil != "" {
		if _, err := time.Parse("2006-01-02", r.ValidUntil); err != nil {
			return "valid_until must be YYYY-MM-DD"
		}
	}
	return ""
}

func (r *PromoRequest) args() []any {
	once, active := true, true
	if r.OncePerUser != nil {
		once = *r.OncePerUser
	}
	if r.IsActive != nil {
		active = *r.IsActive
	}
	var validUntil *string
	if r.ValidUntil != "" {
		validUntil = &r.ValidUntil
	}
	return []any{r.Code, r.Kind, r.Value, r.MinTotal, r.MaxUses, once, validUntil, active, r.Description}
}

const promoSelect = `SELECT p.id, p.code, p.kind, p.value, p.min_total, p.max_uses, p.once_per_user,
	to_char(p.valid_until, 'YYYY-MM-DD'), p.is_active, p.description,
	count(o.id), COALESCE(sum(o.discount), 0)
	FROM promo_codes p
	LEFT JOIN orders o ON o.promo_code_id = p.id AND o.status <> 'cancelled' `

func scanPromos(rows pgx.Rows) ([]PromoCode, error) {
	defer rows.Close()
	list := []PromoCode{}
	for rows.Next() {
		var p PromoCode
		if err := rows.Scan(&p.ID, &p.Code, &p.Kind, &p.Value, &p.MinTotal, &p.MaxUses, &p.OncePerUser,
			&p.ValidUntil, &p.IsActive, &p.Description, &p.Uses, &p.Discounted); err != nil {
			return nil, err
		}
		list = append(list, p)
	}
	return list, rows.Err()
}

func (h *PromoHandler) loadPromo(c *gin.Context, id int) (*PromoCode, error) {
	rows, err := h.DB.Query(c, promoSelect+`WHERE p.id = $1 GROUP BY p.id`, id)
	if err != nil {
		return nil, err
	}
	list, err := scanPromos(rows)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// ListPromos — GET /api/admin/promo
func (h *PromoHandler) ListPromos(c *gin.Context) {
	rows, err := h.DB.Query(c, promoSelect+`GROUP BY p.id ORDER BY p.is_active DESC, p.created_at DESC`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load promo codes"})
		return
	}
	list, err := scanPromos(rows)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load promo codes"})
		return
	}
	c.JSON(http.StatusOK, list)
}

// CreatePromo — POST /api/admin/promo
func (h *PromoHandler) CreatePromo(c *gin.Context) {
	var req PromoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid promo code data"})
		return
	}
	if msg := req.validate(); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	var id int
	err := h.DB.QueryRow(c,
		`INSERT INTO promo_codes (code, kind, value, min_total, max_uses, once_per_user, valid_until, is_active, description)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`, req.args()...).Scan(&id)
	if isPgError(err, "23505") {
		c.JSON(http.StatusConflict, gin.H{"error": "promo code already exists"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create promo code"})
		return
	}
	promo, err := h.loadPromo(c, id)
	if err != nil || promo == nil {
		c.JSON(http.StatusCreated, gin.H{"id": id, "code": req.Code})
		return
	}
	c.JSON(http.StatusCreated, promo)
}

// UpdatePromo — PUT /api/admin/promo/:id (all fields, like the dish form)
func (h *PromoHandler) UpdatePromo(c *gin.Context) {
	id, ok := parseID(c, "promo code")
	if !ok {
		return
	}
	var req PromoRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid promo code data"})
		return
	}
	if msg := req.validate(); msg != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": msg})
		return
	}

	tag, err := h.DB.Exec(c,
		`UPDATE promo_codes SET code = $1, kind = $2, value = $3, min_total = $4, max_uses = $5,
		        once_per_user = $6, valid_until = $7, is_active = $8, description = $9
		 WHERE id = $10`, append(req.args(), id)...)
	if isPgError(err, "23505") {
		c.JSON(http.StatusConflict, gin.H{"error": "promo code already exists"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update promo code"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "promo code not found"})
		return
	}
	promo, err := h.loadPromo(c, id)
	if err != nil || promo == nil {
		c.JSON(http.StatusOK, gin.H{"id": id, "code": req.Code})
		return
	}
	c.JSON(http.StatusOK, promo)
}

// DeletePromo — DELETE /api/admin/promo/:id
// A code that orders already used stays for the history; it can only be turned off.
func (h *PromoHandler) DeletePromo(c *gin.Context) {
	id, ok := parseID(c, "promo code")
	if !ok {
		return
	}
	tag, err := h.DB.Exec(c, `DELETE FROM promo_codes WHERE id = $1`, id)
	if isPgError(err, "23503") {
		c.JSON(http.StatusConflict, gin.H{"error": "promo code was used, turn it off instead"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete promo code"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "promo code not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "promo code deleted"})
}
