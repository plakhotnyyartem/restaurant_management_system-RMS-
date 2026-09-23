package handlers

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OrderHandler struct {
	DB *pgxpool.Pool

	// Notify receives order events (the Telegram bot). nil = nobody is notified.
	Notify OrderNotifier
}

// OrderNotifier is told about order events after they are saved.
// Implementations must return quickly (send in the background).
type OrderNotifier interface {
	OrderCreated(orderID int)
	OrderStatusChanged(orderID int, status string)
}

type OrderItem struct {
	DishID   int     `json:"dish_id"`
	Name     string  `json:"name"`
	Quantity int     `json:"quantity"`
	Price    float64 `json:"price"`
}

type Order struct {
	ID         int         `json:"id"`
	UserID     int         `json:"user_id"`
	UserName   string      `json:"user_name,omitempty"`
	Status     string      `json:"status"`
	TotalPrice float64     `json:"total_price"`
	CreatedAt  time.Time   `json:"created_at"`
	Items      []OrderItem `json:"items"`
}

// ---------- Order lifecycle ----------
//
//	pending → confirmed → preparing → ready → completed
//	   └──→ cancelled

var orderTransitions = map[string][]string{
	"pending":   {"confirmed", "cancelled"},
	"confirmed": {"preparing"},
	"preparing": {"ready"},
	"ready":     {"completed"},
}

// Which role may move an order INTO which status.
var statusRoles = map[string][]string{
	"confirmed": {"waiter", "admin"},
	"preparing": {"cook", "admin"},
	"ready":     {"cook", "admin"},
	"completed": {"waiter", "admin"},
	"cancelled": {"waiter", "admin"},
}

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

// ---------- Loading orders ----------

// loadOrders runs a query returning order rows and attaches their items
// with a second query (two queries instead of one per order).
func (h *OrderHandler) loadOrders(ctx context.Context, query string, args ...any) ([]Order, error) {
	rows, err := h.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}

	orders := []Order{}
	index := map[int]int{}
	var ids []int
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.UserName, &o.Status, &o.TotalPrice, &o.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		o.Items = []OrderItem{}
		index[o.ID] = len(orders)
		ids = append(ids, o.ID)
		orders = append(orders, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return orders, nil
	}

	itemRows, err := h.DB.Query(ctx,
		`SELECT oi.order_id, oi.dish_id, d.name, oi.quantity, oi.price
		 FROM order_items oi
		 JOIN dishes d ON d.id = oi.dish_id
		 WHERE oi.order_id = ANY($1)
		 ORDER BY oi.id`,
		ids,
	)
	if err != nil {
		return nil, err
	}
	defer itemRows.Close()

	for itemRows.Next() {
		var orderID int
		var item OrderItem
		if err := itemRows.Scan(&orderID, &item.DishID, &item.Name, &item.Quantity, &item.Price); err != nil {
			return nil, err
		}
		o := &orders[index[orderID]]
		o.Items = append(o.Items, item)
	}
	return orders, itemRows.Err()
}

// created_at is "timestamp without time zone" in the database's local time;
// AT TIME ZONE turns it into a real moment, so JSON gets a correct offset.
const orderSelect = `SELECT o.id, o.user_id, u.name, o.status, o.total_price,
	o.created_at AT TIME ZONE current_setting('TimeZone')
	FROM orders o JOIN users u ON u.id = o.user_id `

// ---------- Customer endpoints ----------

type CreateOrderRequest struct {
	Items []struct {
		DishID   int `json:"dish_id" binding:"required"`
		Quantity int `json:"quantity" binding:"required,min=1,max=50"`
	} `json:"items" binding:"required,min=1,max=30,dive"`
}

// CreateOrder — POST /api/orders
// Prices are read from the database, never taken from the client.
func (h *OrderHandler) CreateOrder(c *gin.Context) {
	userID, _ := currentUser(c)

	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "items with dish_id and quantity (1-50) are required",
		})
		return
	}

	// Merge duplicate dishes: [{1,2},{1,1}] → {1: 3}
	quantities := map[int]int{}
	var dishIDs []int
	for _, item := range req.Items {
		if _, seen := quantities[item.DishID]; !seen {
			dishIDs = append(dishIDs, item.DishID)
		}
		quantities[item.DishID] += item.Quantity
	}

	tx, err := h.DB.Begin(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order"})
		return
	}
	defer tx.Rollback(c) // no-op after Commit

	type priced struct{ price, cost float64 }
	prices := map[int]priced{}

	rows, err := tx.Query(c,
		`SELECT id, price, cost_price FROM dishes
		 WHERE id = ANY($1) AND COALESCE(is_available, true)`,
		dishIDs,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order"})
		return
	}
	for rows.Next() {
		var id int
		var p priced
		if err := rows.Scan(&id, &p.price, &p.cost); err != nil {
			rows.Close()
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order"})
			return
		}
		prices[id] = p
	}
	rows.Close()

	var total float64
	for _, id := range dishIDs {
		p, ok := prices[id]
		if !ok {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":   "dish is not available",
				"dish_id": id,
			})
			return
		}
		total += p.price * float64(quantities[id])
	}

	var orderID int
	err = tx.QueryRow(c,
		`INSERT INTO orders (user_id, status, total_price) VALUES ($1, 'pending', $2) RETURNING id`,
		userID, total,
	).Scan(&orderID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order"})
		return
	}

	for _, id := range dishIDs {
		_, err = tx.Exec(c,
			`INSERT INTO order_items (order_id, dish_id, quantity, price, cost_price)
			 VALUES ($1, $2, $3, $4, $5)`,
			orderID, id, quantities[id], prices[id].price, prices[id].cost,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order"})
			return
		}
	}

	if err := tx.Commit(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create order"})
		return
	}

	if h.Notify != nil {
		h.Notify.OrderCreated(orderID)
	}

	orders, err := h.loadOrders(c, orderSelect+`WHERE o.id = $1`, orderID)
	if err != nil || len(orders) == 0 {
		c.JSON(http.StatusCreated, gin.H{"id": orderID, "total_price": total, "status": "pending"})
		return
	}
	c.JSON(http.StatusCreated, orders[0])
}

// GetMyOrders — GET /api/orders
func (h *OrderHandler) GetMyOrders(c *gin.Context) {
	userID, _ := currentUser(c)

	orders, err := h.loadOrders(c,
		orderSelect+`WHERE o.user_id = $1 ORDER BY o.created_at DESC LIMIT $2`,
		userID, intQuery(c, "limit", 50, 1, 200),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get orders"})
		return
	}
	c.JSON(http.StatusOK, orders)
}

// GetOrder — GET /api/orders/:id (own orders; staff can open any)
func (h *OrderHandler) GetOrder(c *gin.Context) {
	id, ok := parseID(c, "order")
	if !ok {
		return
	}
	userID, role := currentUser(c)
	isStaff := contains([]string{"admin", "owner", "waiter", "cook"}, role)

	orders, err := h.loadOrders(c,
		orderSelect+`WHERE o.id = $1 AND ($2 OR o.user_id = $3)`,
		id, isStaff, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get order"})
		return
	}
	if len(orders) == 0 {
		// Same answer for "doesn't exist" and "belongs to someone else".
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	c.JSON(http.StatusOK, orders[0])
}

// CancelMyOrder — POST /api/orders/:id/cancel (only while it is still pending)
func (h *OrderHandler) CancelMyOrder(c *gin.Context) {
	id, ok := parseID(c, "order")
	if !ok {
		return
	}
	userID, _ := currentUser(c)

	tag, err := h.DB.Exec(c,
		`UPDATE orders SET status = 'cancelled'
		 WHERE id = $1 AND user_id = $2 AND status = 'pending'`,
		id, userID,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to cancel order"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "only your pending orders can be cancelled"})
		return
	}
	if h.Notify != nil {
		h.Notify.OrderStatusChanged(id, "cancelled")
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "status": "cancelled"})
}

// ---------- Staff endpoints ----------

// GetAllOrders — GET /api/admin/orders?status=pending&limit=50
func (h *OrderHandler) GetAllOrders(c *gin.Context) {
	status := c.Query("status")
	if status != "" && status != "active" && !contains([]string{"pending", "confirmed", "preparing", "ready", "completed", "cancelled"}, status) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown status"})
		return
	}

	// "active" = everything the staff still has to work on.
	orders, err := h.loadOrders(c,
		orderSelect+`WHERE ($1 = ''
		    OR ($1 = 'active' AND o.status IN ('pending', 'confirmed', 'preparing', 'ready'))
		    OR o.status = $1)
		 ORDER BY o.created_at DESC
		 LIMIT $2`,
		status, intQuery(c, "limit", 50, 1, 200),
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get orders"})
		return
	}
	c.JSON(http.StatusOK, orders)
}

type StatusRequest struct {
	Status string `json:"status" binding:"required"`
}

// UpdateStatus — PUT /api/admin/orders/:id/status
func (h *OrderHandler) UpdateStatus(c *gin.Context) {
	id, ok := parseID(c, "order")
	if !ok {
		return
	}
	_, role := currentUser(c)

	var req StatusRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status is required"})
		return
	}

	if !contains(statusRoles[req.Status], role) {
		c.JSON(http.StatusForbidden, gin.H{"error": "your role cannot set this status"})
		return
	}

	var current string
	err := h.DB.QueryRow(c, `SELECT status FROM orders WHERE id = $1`, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "order not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order"})
		return
	}

	if !contains(orderTransitions[current], req.Status) {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "invalid status transition",
			"from":    current,
			"to":      req.Status,
			"allowed": orderTransitions[current],
		})
		return
	}

	tx, err := h.DB.Begin(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order"})
		return
	}
	defer tx.Rollback(c)

	// "AND status = $3" protects against two employees changing the order at once.
	tag, err := tx.Exec(c,
		`UPDATE orders SET status = $1 WHERE id = $2 AND status = $3`,
		req.Status, id, current,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order"})
		return
	}
	if tag.RowsAffected() == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "order was changed by someone else, reload it"})
		return
	}

	// The cook starts cooking → ingredients leave the warehouse (by the recipes).
	// Stock never goes below zero: a missing stocktaking should not block the kitchen.
	if req.Status == "preparing" {
		_, err = tx.Exec(c,
			`UPDATE stock s
			 SET quantity = GREATEST(s.quantity - used.qty, 0), updated_at = CURRENT_TIMESTAMP
			 FROM (
			     SELECT ri.ingredient_id, sum(ri.quantity * oi.quantity) AS qty
			     FROM order_items oi
			     JOIN recipe_items ri ON ri.dish_id = oi.dish_id
			     WHERE oi.order_id = $1
			     GROUP BY ri.ingredient_id
			 ) AS used
			 WHERE s.ingredient_id = used.ingredient_id`,
			id,
		)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to write off ingredients"})
			return
		}
	}

	if err := tx.Commit(c); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order"})
		return
	}
	if h.Notify != nil {
		h.Notify.OrderStatusChanged(id, req.Status)
	}

	c.JSON(http.StatusOK, gin.H{
		"id":     id,
		"status": req.Status,
	})
}
