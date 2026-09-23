package handlers

import (
	"errors"
	"math"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RecipeHandler — recipes (tech cards) and the ingredient catalogue.
//
// A recipe says how much of every ingredient one portion needs. It drives
// three things: the cost price of the dish (→ menu engineering margins),
// the stock write-off when cooking starts and the purchase plan.
type RecipeHandler struct {
	DB *pgxpool.Pool
}

// ---------- Recipes ----------

type recipeSummary struct {
	DishID      int     `json:"dish_id"`
	Name        string  `json:"name"`
	Category    string  `json:"category"`
	Price       float64 `json:"price"`
	CostPrice   float64 `json:"cost_price"`
	IsAvailable bool    `json:"is_available"`
	Items       int     `json:"items"`
	RecipeCost  float64 `json:"recipe_cost"`
}

// ListRecipes — GET /api/admin/recipes
// Every dish with the size and cost of its recipe; dishes without one come first.
func (h *RecipeHandler) ListRecipes(c *gin.Context) {
	rows, err := h.DB.Query(c,
		`SELECT d.id, d.name, c.name, d.price, d.cost_price, COALESCE(d.is_available, true),
		        count(ri.ingredient_id), COALESCE(sum(ri.quantity * i.price), 0)
		 FROM dishes d
		 JOIN categories c ON c.id = d.category_id
		 LEFT JOIN recipe_items ri ON ri.dish_id = d.id
		 LEFT JOIN ingredients i ON i.id = ri.ingredient_id
		 GROUP BY d.id, c.name, c.id
		 ORDER BY count(ri.ingredient_id) > 0, c.id, d.id`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get recipes"})
		return
	}
	defer rows.Close()

	list := []recipeSummary{}
	for rows.Next() {
		var r recipeSummary
		if err := rows.Scan(&r.DishID, &r.Name, &r.Category, &r.Price, &r.CostPrice, &r.IsAvailable, &r.Items, &r.RecipeCost); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read recipes"})
			return
		}
		r.RecipeCost = round2(r.RecipeCost)
		list = append(list, r)
	}
	c.JSON(http.StatusOK, list)
}

type recipeItem struct {
	IngredientID int     `json:"ingredient_id"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Price        float64 `json:"price"` // ₸ per unit
	Quantity     float64 `json:"quantity"`
	Cost         float64 `json:"cost"` // quantity × price
}

type recipeDetail struct {
	DishID     int          `json:"dish_id"`
	Name       string       `json:"name"`
	Price      float64      `json:"price"`
	CostPrice  float64      `json:"cost_price"`
	Items      []recipeItem `json:"items"`
	RecipeCost float64      `json:"recipe_cost"`
}

func (h *RecipeHandler) loadRecipe(c *gin.Context, dishID int) (recipeDetail, error) {
	r := recipeDetail{DishID: dishID, Items: []recipeItem{}}
	err := h.DB.QueryRow(c, `SELECT name, price, cost_price FROM dishes WHERE id = $1`, dishID).
		Scan(&r.Name, &r.Price, &r.CostPrice)
	if err != nil {
		return r, err
	}

	rows, err := h.DB.Query(c,
		`SELECT i.id, i.name, i.unit, i.price, ri.quantity
		 FROM recipe_items ri JOIN ingredients i ON i.id = ri.ingredient_id
		 WHERE ri.dish_id = $1
		 ORDER BY ri.quantity * i.price DESC`, dishID)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var it recipeItem
		if err := rows.Scan(&it.IngredientID, &it.Name, &it.Unit, &it.Price, &it.Quantity); err != nil {
			return r, err
		}
		it.Cost = round2(it.Quantity * it.Price)
		r.RecipeCost += it.Cost
		r.Items = append(r.Items, it)
	}
	r.RecipeCost = round2(r.RecipeCost)
	return r, rows.Err()
}

// GetRecipe — GET /api/admin/dishes/:id/recipe
func (h *RecipeHandler) GetRecipe(c *gin.Context) {
	id, ok := parseID(c, "dish")
	if !ok {
		return
	}
	r, err := h.loadRecipe(c, id)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "dish not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get recipe"})
		return
	}
	c.JSON(http.StatusOK, r)
}

type RecipeRequest struct {
	Items []struct {
		IngredientID int     `json:"ingredient_id" binding:"required"`
		Quantity     float64 `json:"quantity" binding:"gt=0,lte=100"`
	} `json:"items" binding:"max=50,dive"`
	// true — also set the dish cost price to the recipe cost
	UpdateCost bool `json:"update_cost"`
}

// SaveRecipe — PUT /api/admin/dishes/:id/recipe
// Replaces the whole recipe in one transaction. An empty list removes it.
func (h *RecipeHandler) SaveRecipe(c *gin.Context) {
	id, ok := parseID(c, "dish")
	if !ok {
		return
	}
	var req RecipeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items need ingredient_id and a quantity between 0 and 100 per portion"})
		return
	}
	seen := map[int]bool{}
	for _, it := range req.Items {
		if seen[it.IngredientID] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "an ingredient is listed twice", "ingredient_id": it.IngredientID})
			return
		}
		seen[it.IngredientID] = true
	}

	tx, err := h.DB.Begin(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save recipe"})
		return
	}
	defer tx.Rollback(c)

	// Lock the dish row: two cooks saving at once must not mix their recipes.
	var exists int
	if err := tx.QueryRow(c, `SELECT id FROM dishes WHERE id = $1 FOR UPDATE`, id).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "dish not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save recipe"})
		}
		return
	}

	if _, err := tx.Exec(c, `DELETE FROM recipe_items WHERE dish_id = $1`, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save recipe"})
		return
	}
	for _, it := range req.Items {
		_, err := tx.Exec(c,
			`INSERT INTO recipe_items (dish_id, ingredient_id, quantity) VALUES ($1, $2, $3)`,
			id, it.IngredientID, it.Quantity)
		if isPgError(err, "23503") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ingredient not found", "ingredient_id": it.IngredientID})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save recipe"})
			return
		}
	}

	if req.UpdateCost && len(req.Items) > 0 {
		_, err := tx.Exec(c,
			`UPDATE dishes SET cost_price = (
			     SELECT round(sum(ri.quantity * i.price), 2)
			     FROM recipe_items ri JOIN ingredients i ON i.id = ri.ingredient_id
			     WHERE ri.dish_id = $1)
			 WHERE id = $1`, id)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update cost price"})
			return
		}
	}

	if err := tx.Commit(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save recipe"})
		return
	}

	r, err := h.loadRecipe(c, id)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"dish_id": id})
		return
	}
	c.JSON(http.StatusOK, r)
}

// ---------- Ingredient catalogue ----------

type IngredientRequest struct {
	Name      string  `json:"name" binding:"required,max=100"`
	Unit      string  `json:"unit" binding:"required,oneof=кг л шт"`
	Price     float64 `json:"price" binding:"gte=0"`
	PackSize  float64 `json:"pack_size" binding:"gt=0"`
	ShelfLife int     `json:"shelf_life_days" binding:"gt=0,lte=3650"`
}

var errIngredientBody = gin.H{"error": "name, unit (кг, л or шт), a non-negative price, pack size and shelf life are required"}

// ListIngredients — GET /api/admin/ingredients
func (h *RecipeHandler) ListIngredients(c *gin.Context) {
	rows, err := h.DB.Query(c,
		`SELECT i.id, i.name, i.unit, i.price, i.pack_size, i.shelf_life_days, COALESCE(s.quantity, 0),
		        (SELECT count(*) FROM recipe_items ri WHERE ri.ingredient_id = i.id)
		 FROM ingredients i LEFT JOIN stock s ON s.ingredient_id = i.id
		 ORDER BY i.name`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get ingredients"})
		return
	}
	defer rows.Close()

	type row struct {
		ingredient
		UsedIn int `json:"used_in"` // number of dishes whose recipe uses it
	}
	list := []row{}
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Name, &r.Unit, &r.Price, &r.PackSize, &r.ShelfLife, &r.Stock, &r.UsedIn); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read ingredients"})
			return
		}
		list = append(list, r)
	}
	c.JSON(http.StatusOK, list)
}

func bindIngredient(c *gin.Context) (IngredientRequest, bool) {
	var req IngredientRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, errIngredientBody)
		return req, false
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Price = math.Round(req.Price*100) / 100
	return req, true
}

// CreateIngredient — POST /api/admin/ingredients
func (h *RecipeHandler) CreateIngredient(c *gin.Context) {
	req, ok := bindIngredient(c)
	if !ok {
		return
	}
	var id int
	err := h.DB.QueryRow(c,
		`INSERT INTO ingredients (name, unit, price, pack_size, shelf_life_days)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		req.Name, req.Unit, req.Price, req.PackSize, req.ShelfLife).Scan(&id)
	if isPgError(err, "23505") {
		c.JSON(http.StatusConflict, gin.H{"error": "ingredient already exists"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create ingredient"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"id": id, "name": req.Name, "unit": req.Unit, "price": req.Price,
		"pack_size": req.PackSize, "shelf_life_days": req.ShelfLife})
}

// UpdateIngredient — PUT /api/admin/ingredients/:id
// A new price changes the recipe cost of every dish that uses the ingredient
// (their stored cost price is updated when the recipe is saved again).
func (h *RecipeHandler) UpdateIngredient(c *gin.Context) {
	id, ok := parseID(c, "ingredient")
	if !ok {
		return
	}
	req, ok := bindIngredient(c)
	if !ok {
		return
	}
	tag, err := h.DB.Exec(c,
		`UPDATE ingredients SET name = $1, unit = $2, price = $3, pack_size = $4, shelf_life_days = $5
		 WHERE id = $6`,
		req.Name, req.Unit, req.Price, req.PackSize, req.ShelfLife, id)
	if isPgError(err, "23505") {
		c.JSON(http.StatusConflict, gin.H{"error": "ingredient already exists"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update ingredient"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "ingredient not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "name": req.Name, "unit": req.Unit, "price": req.Price,
		"pack_size": req.PackSize, "shelf_life_days": req.ShelfLife})
}

// DeleteIngredient — DELETE /api/admin/ingredients/:id
func (h *RecipeHandler) DeleteIngredient(c *gin.Context) {
	id, ok := parseID(c, "ingredient")
	if !ok {
		return
	}
	tag, err := h.DB.Exec(c, `DELETE FROM ingredients WHERE id = $1`, id)
	// recipe_items has no ON DELETE CASCADE on purpose: a recipe must not lose
	// a component silently.
	if isPgError(err, "23503") {
		c.JSON(http.StatusConflict, gin.H{"error": "ingredient is used in recipes, remove it from them first"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete ingredient"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "ingredient not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "ingredient deleted"})
}
