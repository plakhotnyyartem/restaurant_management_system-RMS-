package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CategoryHandler struct {
	DB *pgxpool.Pool
}

func (h *CategoryHandler) GetCategories(c *gin.Context) {
	rows, err := h.DB.Query(
		c,
		`SELECT id, name FROM categories ORDER BY id`,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to get categories",
		})
		return
	}
	defer rows.Close()

	categories := []gin.H{}

	for rows.Next() {
		var id int
		var name string

		err := rows.Scan(&id, &name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "failed to read categories",
			})
			return
		}

		categories = append(categories, gin.H{
			"id":   id,
			"name": name,
		})
	}

	c.JSON(http.StatusOK, categories)
}

type CreateCategoryRequest struct {
	Name string `json:"name" binding:"required"`
}

func (h *CategoryHandler) CreateCategory(c *gin.Context) {
	var req CreateCategoryRequest

	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "name is required",
		})
		return
	}
	req.Name = strings.TrimSpace(req.Name)

	var id int

	err := h.DB.QueryRow(
		c,
		`INSERT INTO categories (name)
		 VALUES ($1)
		 RETURNING id`,
		req.Name,
	).Scan(&id)

	if isPgError(err, "23505") {
		c.JSON(http.StatusConflict, gin.H{
			"error": "category already exists",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to create category",
		})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"id":   id,
		"name": req.Name,
	})
}

func (h *CategoryHandler) UpdateCategory(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid category id",
		})
		return
	}

	var req CreateCategoryRequest

	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "name is required",
		})
		return
	}
	req.Name = strings.TrimSpace(req.Name)

	err = h.DB.QueryRow(
		c,
		`UPDATE categories
		 SET name = $1
		 WHERE id = $2
		 RETURNING id`,
		req.Name,
		id,
	).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "category not found",
		})
		return
	}
	if isPgError(err, "23505") {
		c.JSON(http.StatusConflict, gin.H{
			"error": "category already exists",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to update category",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":   id,
		"name": req.Name,
	})
}

func (h *CategoryHandler) DeleteCategory(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "invalid category id",
		})
		return
	}

	tag, err := h.DB.Exec(
		c,
		`DELETE FROM categories WHERE id = $1`,
		id,
	)

	if isPgError(err, "23503") {
		c.JSON(http.StatusConflict, gin.H{
			"error": "category has dishes, remove or move them first",
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "failed to delete category",
		})
		return
	}

	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{
			"error": "category not found",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "category deleted",
	})
}

// isPgError reports whether err is a PostgreSQL error with the given SQLSTATE code
// (23505 = unique_violation, 23503 = foreign_key_violation).
func isPgError(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
