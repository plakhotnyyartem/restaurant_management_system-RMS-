package handlers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InventoryHandler — warehouse and purchase planning.
type InventoryHandler struct {
	DB        *pgxpool.Pool
	Analytics *AnalyticsHandler // demand forecast comes from the analytics module
}

type ingredient struct {
	ID        int     `json:"id"`
	Name      string  `json:"name"`
	Unit      string  `json:"unit"`
	Price     float64 `json:"price"`
	PackSize  float64 `json:"pack_size"`
	ShelfLife int     `json:"shelf_life_days"`
	Stock     float64 `json:"stock"`
}

type recipeLine struct {
	DishID, IngredientID int
	Quantity             float64
}

func (h *InventoryHandler) loadIngredients(ctx context.Context) ([]ingredient, error) {
	rows, err := h.DB.Query(ctx,
		`SELECT i.id, i.name, i.unit, i.price, i.pack_size, i.shelf_life_days, COALESCE(s.quantity, 0)
		 FROM ingredients i
		 LEFT JOIN stock s ON s.ingredient_id = i.id
		 ORDER BY i.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []ingredient{}
	for rows.Next() {
		var i ingredient
		if err := rows.Scan(&i.ID, &i.Name, &i.Unit, &i.Price, &i.PackSize, &i.ShelfLife, &i.Stock); err != nil {
			return nil, err
		}
		list = append(list, i)
	}
	return list, rows.Err()
}

func (h *InventoryHandler) loadRecipes(ctx context.Context) ([]recipeLine, error) {
	rows, err := h.DB.Query(ctx, `SELECT dish_id, ingredient_id, quantity FROM recipe_items`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []recipeLine
	for rows.Next() {
		var r recipeLine
		if err := rows.Scan(&r.DishID, &r.IngredientID, &r.Quantity); err != nil {
			return nil, err
		}
		lines = append(lines, r)
	}
	return lines, rows.Err()
}

// weeklyNeed: how much of every ingredient the forecast demand for the next 7 days requires.
func weeklyNeed(recipes []recipeLine, demand map[int]float64) map[int]float64 {
	need := map[int]float64{}
	for _, r := range recipes {
		need[r.IngredientID] += r.Quantity * demand[r.DishID]
	}
	return need
}

func (h *InventoryHandler) forecastDemand(ctx context.Context) (map[int]float64, []dishForecast, error) {
	fc, err := h.Analytics.forecast(ctx, 8)
	if err != nil {
		return nil, nil, err
	}
	demand := map[int]float64{}
	for _, d := range fc.Dishes {
		demand[d.ID] = d.NextWeek
	}
	return demand, fc.Dishes, nil
}

// ---------- Warehouse ----------

type inventoryRow struct {
	ingredient
	WeeklyNeed float64  `json:"weekly_need"`
	DaysCover  *float64 `json:"days_cover"` // how many days the stock lasts; nil = not used by any dish
	Status     string   `json:"status"`     // ok | low | critical | unused
}

// GetInventory — GET /api/admin/inventory
func (h *InventoryHandler) GetInventory(c *gin.Context) {
	ingredients, err := h.loadIngredients(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load inventory"})
		return
	}
	recipes, err := h.loadRecipes(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load recipes"})
		return
	}
	demand, _, err := h.forecastDemand(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build forecast"})
		return
	}
	need := weeklyNeed(recipes, demand)

	rows := make([]inventoryRow, 0, len(ingredients))
	for _, i := range ingredients {
		row := inventoryRow{ingredient: i, WeeklyNeed: round2(need[i.ID]), Status: "unused"}
		if daily := need[i.ID] / 7; daily > 0 {
			days := round2(i.Stock / daily)
			row.DaysCover = &days
			switch {
			case days < 2:
				row.Status = "critical"
			case days < 5:
				row.Status = "low"
			default:
				row.Status = "ok"
			}
		}
		rows = append(rows, row)
	}
	c.JSON(http.StatusOK, rows)
}

type ReceiveRequest struct {
	Items []struct {
		IngredientID int     `json:"ingredient_id" binding:"required"`
		Quantity     float64 `json:"quantity" binding:"gt=0"`
	} `json:"items" binding:"required,min=1,dive"`
}

// Receive — POST /api/admin/inventory/receive
// A delivery arrived: add the quantities to the stock (one transaction for the whole delivery).
func (h *InventoryHandler) Receive(c *gin.Context) {
	var req ReceiveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "items with ingredient_id and a positive quantity are required"})
		return
	}

	tx, err := h.DB.Begin(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to receive delivery"})
		return
	}
	defer tx.Rollback(c)

	for _, item := range req.Items {
		_, err := tx.Exec(c,
			`INSERT INTO stock (ingredient_id, quantity, updated_at) VALUES ($1, $2, CURRENT_TIMESTAMP)
			 ON CONFLICT (ingredient_id)
			 DO UPDATE SET quantity = stock.quantity + EXCLUDED.quantity, updated_at = CURRENT_TIMESTAMP`,
			item.IngredientID, item.Quantity)
		if isPgError(err, "23503") {
			c.JSON(http.StatusBadRequest, gin.H{"error": "ingredient not found", "ingredient_id": item.IngredientID})
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to receive delivery"})
			return
		}
	}
	if err := tx.Commit(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to receive delivery"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"received": len(req.Items)})
}

type StockRequest struct {
	Quantity *float64 `json:"quantity" binding:"required,gte=0"`
}

// SetStock — PUT /api/admin/inventory/:id (stocktaking: the real counted amount)
func (h *InventoryHandler) SetStock(c *gin.Context) {
	id, ok := parseID(c, "ingredient")
	if !ok {
		return
	}
	var req StockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a non-negative quantity is required"})
		return
	}

	_, err := h.DB.Exec(c,
		`INSERT INTO stock (ingredient_id, quantity, updated_at) VALUES ($1, $2, CURRENT_TIMESTAMP)
		 ON CONFLICT (ingredient_id) DO UPDATE SET quantity = EXCLUDED.quantity, updated_at = CURRENT_TIMESTAMP`,
		id, *req.Quantity)
	if isPgError(err, "23503") {
		c.JSON(http.StatusNotFound, gin.H{"error": "ingredient not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update stock"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "stock": *req.Quantity})
}

// ---------- Purchase optimisation (linear programming) ----------
//
// Variables:
//   s_d ≥ 0  portions of dish d we plan to sell next week
//   x_j ≥ 0  units of ingredient j to buy
//
// maximise   Σ price_d · s_d  −  Σ price_j · x_j            (revenue − purchase spend)
// subject to Σ_d r_dj · s_d − x_j ≤ stock_j   for every ingredient   (enough to cook)
//            Σ_j price_j · x_j   ≤ budget                            (money)
//            s_d                 ≤ demand_d  for every dish          (can't sell more than forecast)
//
// The LP gives fractional kilograms; afterwards they are rounded to whole packs
// without exceeding the budget.

type purchaseItem struct {
	IngredientID int     `json:"ingredient_id"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Need         float64 `json:"need"`
	Stock        float64 `json:"stock"`
	Buy          float64 `json:"buy"` // units, multiple of the pack size
	Packs        int     `json:"packs"`
	PackSize     float64 `json:"pack_size"`
	Cost         float64 `json:"cost"`
	ShelfLife    int     `json:"shelf_life_days"`
	Warning      string  `json:"warning,omitempty"`
}

type plannedDish struct {
	ID          int     `json:"id"`
	Name        string  `json:"name"`
	Price       float64 `json:"price"`
	Demand      float64 `json:"demand"`
	Planned     float64 `json:"planned"`
	CoveragePct float64 `json:"coverage_pct"`
}

type budgetPoint struct {
	Budget      float64 `json:"budget"`
	Profit      float64 `json:"profit"`
	CoveragePct float64 `json:"coverage_pct"`
}

type purchasePlan struct {
	Days              int            `json:"days"`
	SafetyPct         int            `json:"safety_pct"`
	Budget            float64        `json:"budget"`
	FullBudget        float64        `json:"full_budget"` // enough to cover the whole forecast
	BudgetLimited     bool           `json:"budget_limited"`
	TotalCost         float64        `json:"total_cost"`
	ExpectedRevenue   float64        `json:"expected_revenue"`
	ExpectedProfit    float64        `json:"expected_profit"`
	CoveragePct       float64        `json:"coverage_pct"`
	BudgetShadowPrice float64        `json:"budget_shadow_price"` // extra profit per +1 ₸ of budget
	Items             []purchaseItem `json:"items"`
	Dishes            []plannedDish  `json:"dishes"`
	Curve             []budgetPoint  `json:"curve"`
	NoRecipe          []string       `json:"no_recipe"`
	Solver            gin.H          `json:"solver"`
}

type lpModel struct {
	dishes      []plannedDish
	ingredients []ingredient
	recipe      map[[2]int]float64 // (dish index, ingredient index) → quantity per portion
}

// solve builds and solves the LP for a given budget.
func (m lpModel) solve(budget float64) (lpResult, error) {
	nd, ni := len(m.dishes), len(m.ingredients)
	c := make([]float64, nd+ni)
	for d, dish := range m.dishes {
		c[d] = dish.Price
	}
	for j, ing := range m.ingredients {
		c[nd+j] = -ing.Price
	}

	var A [][]float64
	var b []float64
	for j, ing := range m.ingredients {
		row := make([]float64, nd+ni)
		for d := range m.dishes {
			row[d] = m.recipe[[2]int{d, j}]
		}
		row[nd+j] = -1
		A = append(A, row)
		b = append(b, ing.Stock)
	}
	budgetRow := make([]float64, nd+ni)
	for j, ing := range m.ingredients {
		budgetRow[nd+j] = ing.Price
	}
	A = append(A, budgetRow)
	b = append(b, budget)
	for d, dish := range m.dishes {
		row := make([]float64, nd+ni)
		row[d] = 1
		A = append(A, row)
		b = append(b, dish.Demand)
	}

	return simplex(c, A, b)
}

func (m lpModel) coverage(res lpResult) float64 {
	var planned, demand float64
	for d, dish := range m.dishes {
		planned += res.X[d]
		demand += dish.Demand
	}
	if demand == 0 {
		return 100
	}
	return round2(planned / demand * 100)
}

// PurchasePlan — GET /api/admin/analytics/purchasing?budget=500000&safety=10
func (h *InventoryHandler) PurchasePlan(c *gin.Context) {
	safety := intQuery(c, "safety", 10, 0, 50)
	budgetParam := float64(intQuery(c, "budget", 0, 0, 1_000_000_000))

	ingredients, err := h.loadIngredients(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load inventory"})
		return
	}
	recipes, err := h.loadRecipes(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load recipes"})
		return
	}
	_, forecastDishes, err := h.forecastDemand(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build forecast"})
		return
	}

	var prices = map[int]float64{}
	rows, err := h.DB.Query(c, `SELECT id, price FROM dishes`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to load dishes"})
		return
	}
	for rows.Next() {
		var id int
		var price float64
		if rows.Scan(&id, &price) == nil {
			prices[id] = price
		}
	}
	rows.Close()

	hasRecipe := map[int]bool{}
	for _, r := range recipes {
		hasRecipe[r.DishID] = true
	}

	// Build the model: dishes with a recipe and some demand, ingredients they use.
	model := lpModel{recipe: map[[2]int]float64{}}
	plan := purchasePlan{Days: 7, SafetyPct: safety, Items: []purchaseItem{}, Dishes: []plannedDish{}, Curve: []budgetPoint{}, NoRecipe: []string{}}
	dishIndex := map[int]int{}
	for _, d := range forecastDishes {
		if !hasRecipe[d.ID] {
			plan.NoRecipe = append(plan.NoRecipe, d.Name)
			continue
		}
		if d.NextWeek <= 0 {
			continue
		}
		dishIndex[d.ID] = len(model.dishes)
		model.dishes = append(model.dishes, plannedDish{
			ID: d.ID, Name: d.Name, Price: prices[d.ID],
			Demand: math.Ceil(d.NextWeek * (1 + float64(safety)/100)), // safety stock on top of the forecast
		})
	}
	used := map[int]bool{}
	for _, r := range recipes {
		if _, ok := dishIndex[r.DishID]; ok {
			used[r.IngredientID] = true
		}
	}
	ingIndex := map[int]int{}
	for _, ing := range ingredients {
		if used[ing.ID] {
			ingIndex[ing.ID] = len(model.ingredients)
			model.ingredients = append(model.ingredients, ing)
		}
	}
	for _, r := range recipes {
		d, okD := dishIndex[r.DishID]
		j, okJ := ingIndex[r.IngredientID]
		if okD && okJ {
			model.recipe[[2]int{d, j}] = r.Quantity
		}
	}

	if len(model.dishes) == 0 {
		c.JSON(http.StatusOK, plan)
		return
	}

	// Budget that covers the whole forecast (rounded up to packs).
	demandByID := map[int]float64{}
	for _, d := range model.dishes {
		demandByID[d.ID] = d.Demand
	}
	need := weeklyNeed(recipes, demandByID)
	for _, ing := range model.ingredients {
		shortage := math.Max(need[ing.ID]-ing.Stock, 0)
		plan.FullBudget += math.Ceil(shortage/ing.PackSize-1e-9) * ing.PackSize * ing.Price
	}
	plan.FullBudget = math.Round(plan.FullBudget)

	budget := budgetParam
	if budget <= 0 {
		budget = plan.FullBudget
	}
	plan.Budget = budget

	res, err := model.solve(budget)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "optimisation failed: " + err.Error()})
		return
	}
	nd := len(model.dishes)
	budgetRow := len(model.ingredients)
	plan.BudgetShadowPrice = round2(res.Duals[budgetRow])
	plan.BudgetLimited = plan.BudgetShadowPrice > 1e-6
	plan.Solver = gin.H{
		"method":      "симплекс-метод (правило Бленда)",
		"variables":   len(res.X),
		"constraints": len(res.Duals),
		"pivots":      res.Pivots,
	}

	// Round kilograms to whole packs: first down, then add packs where the LP
	// wanted the most of a partial pack, as long as the budget allows.
	packs := make([]int, len(model.ingredients))
	var spent float64
	type partial struct {
		j    int
		frac float64
	}
	var partials []partial
	for j, ing := range model.ingredients {
		exact := res.X[nd+j] / ing.PackSize
		packs[j] = int(math.Floor(exact + 1e-9))
		spent += float64(packs[j]) * ing.PackSize * ing.Price
		if f := exact - float64(packs[j]); f > 1e-6 {
			partials = append(partials, partial{j, f})
		}
	}
	sort.Slice(partials, func(a, b int) bool { return partials[a].frac > partials[b].frac })
	for _, p := range partials {
		ing := model.ingredients[p.j]
		if cost := ing.PackSize * ing.Price; spent+cost <= budget+1e-6 {
			packs[p.j]++
			spent += cost
		}
	}

	for j, ing := range model.ingredients {
		item := purchaseItem{
			IngredientID: ing.ID, Name: ing.Name, Unit: ing.Unit,
			Need: round2(need[ing.ID]), Stock: round2(ing.Stock),
			Packs: packs[j], PackSize: ing.PackSize,
			Buy:       round2(float64(packs[j]) * ing.PackSize),
			Cost:      math.Round(float64(packs[j]) * ing.PackSize * ing.Price),
			ShelfLife: ing.ShelfLife,
		}
		if item.Packs > 0 && ing.ShelfLife < plan.Days {
			deliveries := int(math.Ceil(float64(plan.Days) / float64(ing.ShelfLife)))
			item.Warning = fmt.Sprintf("хранится %d дн. — разбить на %d поставки", ing.ShelfLife, deliveries)
		}
		plan.TotalCost += item.Cost
		plan.Items = append(plan.Items, item)
	}
	sort.Slice(plan.Items, func(a, b int) bool { return plan.Items[a].Cost > plan.Items[b].Cost })

	for d, dish := range model.dishes {
		dish.Planned = math.Floor(res.X[d] + 1e-6)
		if dish.Demand > 0 {
			dish.CoveragePct = round2(dish.Planned / dish.Demand * 100)
		}
		plan.ExpectedRevenue += dish.Planned * dish.Price
		plan.Dishes = append(plan.Dishes, dish)
	}
	sort.Slice(plan.Dishes, func(a, b int) bool { return plan.Dishes[a].CoveragePct < plan.Dishes[b].CoveragePct })
	plan.ExpectedProfit = math.Round(plan.ExpectedRevenue - plan.TotalCost)
	plan.CoveragePct = model.coverage(res)

	// "What if" curve: the same LP for 0…100% of the full budget.
	for _, share := range []float64{0, 0.2, 0.4, 0.6, 0.8, 1} {
		b := math.Round(plan.FullBudget * share)
		if r, err := model.solve(b); err == nil {
			plan.Curve = append(plan.Curve, budgetPoint{Budget: b, Profit: math.Round(r.Value), CoveragePct: model.coverage(r)})
		}
	}

	c.JSON(http.StatusOK, plan)
}
