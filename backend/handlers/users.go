package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------- Users: list and staff roles (admin only) ----------

type UserHandler struct {
	DB *pgxpool.Pool
}

var userRoles = map[string]bool{"customer": true, "waiter": true, "cook": true, "admin": true, "owner": true}

type UserRow struct {
	ID          int        `json:"id"`
	Name        string     `json:"name"`
	Email       string     `json:"email"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	Orders      int        `json:"orders"`
	Spent       float64    `json:"spent"` // non-cancelled orders
	LastOrderAt *time.Time `json:"last_order_at"`
}

// ListUsers — GET /api/admin/users?q=&role=&limit=&offset=
// Staff first, then customers by spending; q searches name and email.
func (h *UserHandler) ListUsers(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	role := c.Query("role")
	if role != "" && !userRoles[role] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown role"})
		return
	}
	limit := intQuery(c, "limit", 50, 1, 200)
	offset := intQuery(c, "offset", 0, 0, 1_000_000)
	ctx := c.Request.Context()

	rows, err := h.DB.Query(ctx, `
		WITH filtered AS (
		    SELECT u.* FROM users u
		    WHERE ($1 = '' OR u.name ILIKE '%' || $1 || '%' OR u.email ILIKE '%' || $1 || '%')
		      AND ($2 = '' OR u.role = $2)
		)
		SELECT f.id, f.name, f.email, f.role, COALESCE(f.created_at, now()),
		       COUNT(o.id) FILTER (WHERE o.status <> 'cancelled'),
		       COALESCE(SUM(o.total_price) FILTER (WHERE o.status <> 'cancelled'), 0)::float8,
		       MAX(o.created_at),
		       (SELECT COUNT(*) FROM filtered)
		FROM filtered f
		LEFT JOIN orders o ON o.user_id = f.id
		GROUP BY f.id, f.name, f.email, f.role, f.created_at
		ORDER BY (f.role = 'customer'), 7 DESC, f.id
		LIMIT $3 OFFSET $4`, q, role, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load users"})
		return
	}
	defer rows.Close()

	users := []UserRow{}
	total := 0
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.Role, &u.CreatedAt,
			&u.Orders, &u.Spent, &u.LastOrderAt, &total); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read users"})
			return
		}
		users = append(users, u)
	}
	if rows.Err() != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read users"})
		return
	}

	// How many people have each role — for the filter chips.
	counts := map[string]int{}
	countRows, err := h.DB.Query(ctx, `SELECT role, COUNT(*) FROM users GROUP BY role`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to count users"})
		return
	}
	defer countRows.Close()
	for countRows.Next() {
		var r string
		var n int
		if err := countRows.Scan(&r, &n); err == nil {
			counts[r] = n
		}
	}

	c.JSON(http.StatusOK, gin.H{"users": users, "total": total, "roles": counts})
}

type RoleRequest struct {
	Role string `json:"role" binding:"required"`
}

var (
	errUserNotFound = errors.New("user not found")
	errLastAdmin    = errors.New("cannot remove the last admin")
)

// SetRole — PATCH /api/admin/users/:id/role
// The new role works after the user logs in again: the token keeps the old one.
func (h *UserHandler) SetRole(c *gin.Context) {
	id, ok := parseID(c, "user")
	if !ok {
		return
	}
	var req RoleRequest
	if err := c.ShouldBindJSON(&req); err != nil || !userRoles[req.Role] {
		c.JSON(http.StatusBadRequest, gin.H{"error": "role must be customer, waiter, cook, admin or owner"})
		return
	}
	if me, _ := currentUser(c); me == id {
		// Otherwise an admin could lock themselves out of the panel.
		c.JSON(http.StatusBadRequest, gin.H{"error": "you cannot change your own role"})
		return
	}

	var user UserRow
	err := pgx.BeginFunc(c.Request.Context(), h.DB, func(tx pgx.Tx) error {
		return setRole(c.Request.Context(), tx, id, req.Role, &user)
	})
	switch {
	case errors.Is(err, errUserNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, errLastAdmin):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case err != nil:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to change role"})
	default:
		c.JSON(http.StatusOK, user)
	}
}

func setRole(ctx context.Context, tx pgx.Tx, id int, role string, user *UserRow) error {
	// Lock every admin row: two admins demoting each other at the same moment
	// must not leave the system without an admin.
	rows, err := tx.Query(ctx, `SELECT id FROM users WHERE role = 'admin' ORDER BY id FOR UPDATE`)
	if err != nil {
		return err
	}
	admins := map[int]bool{}
	for rows.Next() {
		var adminID int
		if err := rows.Scan(&adminID); err != nil {
			rows.Close()
			return err
		}
		admins[adminID] = true
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}
	if admins[id] && role != "admin" && len(admins) == 1 {
		return errLastAdmin
	}

	err = tx.QueryRow(ctx, `
		UPDATE users SET role = $2 WHERE id = $1
		RETURNING id, name, email, role, COALESCE(created_at, now())`, id, role).
		Scan(&user.ID, &user.Name, &user.Email, &user.Role, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return errUserNotFound
	}
	return err
}
