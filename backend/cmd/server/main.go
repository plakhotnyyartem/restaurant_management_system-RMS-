package main

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"

	"restaurant-management/database"
	"restaurant-management/handlers"
)

func main() {
	db, err := database.Connect()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	router := gin.Default()

	authHandler := &handlers.AuthHandler{
		DB: db,
	}

	categoryHandler := &handlers.CategoryHandler{
		DB: db,
	}

	dishHandler := &handlers.DishHandler{
		DB: db,
	}

	orderHandler := &handlers.OrderHandler{
		DB: db,
	}

	analyticsHandler := &handlers.AnalyticsHandler{
		DB: db,
	}

	inventoryHandler := &handlers.InventoryHandler{
		DB:        db,
		Analytics: analyticsHandler,
	}

	assistantHandler := &handlers.AssistantHandler{
		Analytics: analyticsHandler,
		Inventory: inventoryHandler,
	}

	router.GET("/api/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":   "ok",
			"database": "connected",
		})
	})

	router.POST("/api/auth/register", authHandler.Register)
	router.POST("/api/auth/login", authHandler.Login)
	router.GET("/api/categories", categoryHandler.GetCategories)
	router.GET("/api/dishes", dishHandler.GetDishes)
	router.GET("/api/dishes/:id", dishHandler.GetDish)
	router.GET("/api/dishes/:id/pairs", analyticsHandler.DishPairs)

	protected := router.Group("/api")
	protected.Use(handlers.AuthMiddleware())

	protected.GET("/profile", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "you are authorized",
			"user_id": c.MustGet("user_id"),
			"role":    c.MustGet("role"),
		})
	})

	// Customer orders
	protected.POST("/orders", orderHandler.CreateOrder)
	protected.GET("/orders", orderHandler.GetMyOrders)
	protected.GET("/orders/:id", orderHandler.GetOrder)
	protected.POST("/orders/:id/cancel", orderHandler.CancelMyOrder)

	// Staff: order queue for waiters and cooks
	// (which status each role may set is checked inside UpdateStatus)
	staff := protected.Group("/admin")
	staff.Use(handlers.RequireRole("admin", "waiter", "cook"))
	staff.GET("/orders", orderHandler.GetAllOrders)
	staff.PUT("/orders/:id/status", orderHandler.UpdateStatus)

	// Analytics and decision support for the owner and admin
	analytics := protected.Group("/admin/analytics")
	analytics.Use(handlers.RequireRole("admin", "owner"))
	analytics.GET("/summary", analyticsHandler.Summary)
	analytics.GET("/sales", analyticsHandler.Sales)
	analytics.GET("/heatmap", analyticsHandler.Heatmap)
	analytics.GET("/menu", analyticsHandler.MenuEngineering)
	analytics.GET("/pairs", analyticsHandler.Pairs)
	analytics.GET("/recommendations", analyticsHandler.Recommendations)
	analytics.GET("/forecast", analyticsHandler.Forecast)
	analytics.GET("/purchasing", inventoryHandler.PurchasePlan)

	// AI assistant: explains the analytics above in plain language
	assistant := protected.Group("/admin/assistant")
	assistant.Use(handlers.RequireRole("admin", "owner"))
	assistant.POST("", assistantHandler.Ask)

	// Warehouse: the cook sees stock and receives deliveries, the owner watches it
	inventory := protected.Group("/admin/inventory")
	inventory.Use(handlers.RequireRole("admin", "owner", "cook"))
	inventory.GET("", inventoryHandler.GetInventory)
	inventoryWrite := protected.Group("/admin/inventory")
	inventoryWrite.Use(handlers.RequireRole("admin", "cook"))
	inventoryWrite.POST("/receive", inventoryHandler.Receive)
	inventoryWrite.PUT("/:id", inventoryHandler.SetStock)

	admin := protected.Group("/admin")
	admin.Use(handlers.RequireRole("admin"))
	admin.POST("/categories", categoryHandler.CreateCategory)
	admin.PUT("/categories/:id", categoryHandler.UpdateCategory)
	admin.DELETE("/categories/:id", categoryHandler.DeleteCategory)

	admin.GET("/dishes", dishHandler.GetAdminDishes)
	admin.POST("/dishes", dishHandler.CreateDish)
	admin.PUT("/dishes/:id", dishHandler.UpdateDish)
	admin.PATCH("/dishes/:id/availability", dishHandler.SetAvailability)
	admin.DELETE("/dishes/:id", dishHandler.DeleteDish)

	admin.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "admin access granted",
		})
	})

	// Everything that is not an API route is served from the frontend folder,
	// so pages and API share one origin (no CORS needed).
	frontend := http.FileServer(http.Dir(frontendDir()))
	router.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "endpoint not found",
			})
			return
		}
		frontend.ServeHTTP(c.Writer, c.Request)
	})

	log.Println("Server started on http://localhost:8080")

	if err := router.Run(":8080"); err != nil {
		log.Fatal(err)
	}
}

// frontendDir finds the frontend folder whether the server is started
// from the repository root or from the backend folder.
func frontendDir() string {
	for _, dir := range []string{"frontend", "../frontend"} {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}
	return "../frontend"
}
