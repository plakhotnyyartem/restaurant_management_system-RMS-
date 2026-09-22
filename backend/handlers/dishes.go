package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DishHandler struct {
	DB *pgxpool.Pool
}

type Dish struct {
	ID          int      `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Price       float64  `json:"price"`
	CostPrice   *float64 `json:"cost_price,omitempty"` // only in admin responses
	CategoryID  int      `json:"category_id"`
	ImageURL    string   `json:"image_url"`
	IsAvailable bool     `json:"is_available"`
}

const dishColumns = `id, name, COALESCE(description, ''), price, cost_price,
	category_id, COALESCE(image_url, ''), COALESCE(is_available, true)`

func scanDish(row pgx.Row, withCost bool) (Dish, error) {
	var d Dish
	var cost float64
	err := row.Scan(&d.ID, &d.Name, &d.Description, &d.Price, &cost,
		&d.CategoryID, &d.ImageURL, &d.IsAvailable)
	if withCost {
		d.CostPrice = &cost
	}
	return d, err
}

func (h *DishHandler) listDishes(c *gin.Context, withCost bool) {
	query := `SELECT ` + dishColumns + ` FROM dishes WHERE ($1 = 0 OR category_id = $1) ORDER BY category_id, id`

	rows, err := h.DB.Query(c, query, intQuery(c, "category_id", 0, 0, 1<<31-1))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to get dishes",
		})
		return
	}
	defer rows.Close()

	dishes := []Dish{}
	for rows.Next() {
		d, err := scanDish(rows, withCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to read dishes",
			})
			return
		}
		dishes = append(dishes, d)
	}

	c.JSON(http.StatusOK, dishes)
}

// GetDishes — public menu. Optional filter: ?category_id=1
func (h *DishHandler) GetDishes(c *gin.Context) {
	h.listDishes(c, false)
}

// GetAdminDishes — same list, but with cost_price for the admin panel.
func (h *DishHandler) GetAdminDishes(c *gin.Context) {
	h.listDishes(c, true)
}

func (h *DishHandler) GetDish(c *gin.Context) {
	id, ok := parseID(c, "dish")
	if !ok {
		return
	}

	d, err := scanDish(h.DB.QueryRow(c, `SELECT `+dishColumns+` FROM dishes WHERE id = $1`, id), false)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "dish not found",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to get dish",
		})
		return
	}

	c.JSON(http.StatusOK, d)
}

type DishRequest struct {
	Name        string  `json:"name" binding:"required"`
	Description string  `json:"description"`
	Price       float64 `json:"price" binding:"gte=0"`
	CostPrice   float64 `json:"cost_price" binding:"gte=0"`
	CategoryID  int     `json:"category_id" binding:"required"`
	ImageURL    string  `json:"image_url"`
	IsAvailable *bool   `json:"is_available"`
}

func bindDish(c *gin.Context) (DishRequest, bool) {
	var req DishRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "name, category_id and a non-negative price are required",
		})
		return req, false
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.IsAvailable == nil {
		available := true
		req.IsAvailable = &available
	}
	return req, true
}

// dishWriteError maps database errors of insert/update to HTTP answers.
func dishWriteError(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		c.JSON(http.StatusNotFound, gin.H{"error": "dish not found"})
	case isPgError(err, "23503"):
		c.JSON(http.StatusBadRequest, gin.H{"error": "category not found"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to " + action + " dish"})
	}
}

func (h *DishHandler) CreateDish(c *gin.Context) {
	req, ok := bindDish(c)
	if !ok {
		return
	}

	d, err := scanDish(h.DB.QueryRow(
		c,
		`INSERT INTO dishes (name, description, price, cost_price, category_id, image_url, is_available)
		 VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7)
		 RETURNING `+dishColumns,
		req.Name, req.Description, req.Price, req.CostPrice, req.CategoryID, req.ImageURL, *req.IsAvailable,
	), true)
	if err != nil {
		dishWriteError(c, err, "create")
		return
	}

	c.JSON(http.StatusCreated, d)
}

func (h *DishHandler) UpdateDish(c *gin.Context) {
	id, ok := parseID(c, "dish")
	if !ok {
		return
	}
	req, ok := bindDish(c)
	if !ok {
		return
	}

	d, err := scanDish(h.DB.QueryRow(
		c,
		`UPDATE dishes
		 SET name = $1, description = $2, price = $3, cost_price = $4,
		     category_id = $5, image_url = NULLIF($6, ''), is_available = $7
		 WHERE id = $8
		 RETURNING `+dishColumns,
		req.Name, req.Description, req.Price, req.CostPrice, req.CategoryID, req.ImageURL, *req.IsAvailable, id,
	), true)
	if err != nil {
		dishWriteError(c, err, "update")
		return
	}

	c.JSON(http.StatusOK, d)
}

type AvailabilityRequest struct {
	IsAvailable *bool `json:"is_available" binding:"required"`
}

// SetAvailability — quick "stop-list" toggle without sending the whole dish.
func (h *DishHandler) SetAvailability(c *gin.Context) {
	id, ok := parseID(c, "dish")
	if !ok {
		return
	}

	var req AvailabilityRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "is_available is required",
		})
		return
	}

	tag, err := h.DB.Exec(c, `UPDATE dishes SET is_available = $1 WHERE id = $2`, *req.IsAvailable, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to update dish",
		})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "dish not found",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":           id,
		"is_available": *req.IsAvailable,
	})
}

func (h *DishHandler) DeleteDish(c *gin.Context) {
	id, ok := parseID(c, "dish")
	if !ok {
		return
	}

	tag, err := h.DB.Exec(c, `DELETE FROM dishes WHERE id = $1`, id)

	// Dishes that were ever ordered stay in the database for the order history
	// and analytics; they can only be hidden from the menu.
	if isPgError(err, "23503") {
		c.JSON(http.StatusConflict, gin.H{
			"error": "dish has orders, make it unavailable instead",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to delete dish",
		})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "dish not found",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "dish deleted",
	})
}
