// Package app wires all HTTP routes. It is separate from cmd/server so that
// tests can build exactly the same router and send real requests to it.
package app

import (
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"restaurant-management/handlers"
)

// NewRouter returns the router with every API route and the frontend.
// bot may be nil when the Telegram bot is not configured.
func NewRouter(db *pgxpool.Pool, bot *handlers.TelegramBot) *gin.Engine {
	router := gin.New()
	router.Use(gin.Logger(), gin.Recovery())
	router.SetTrustedProxies(nil) // not behind a proxy: use the real client address

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
	if bot != nil {
		// Assigned only when the bot exists: a nil *TelegramBot stored in the
		// interface would not be == nil and would crash on the first order.
		orderHandler.Notify = bot
	}

	recipeHandler := &handlers.RecipeHandler{
		DB: db,
	}

	telegramHandler := &handlers.TelegramHandler{
		DB:  db,
		Bot: bot,
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
	router.GET("/api/bot", telegramHandler.PublicInfo)

	protected := router.Group("/api")
	protected.Use(handlers.AuthMiddleware())

	protected.GET("/profile", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "you are authorized",
			"user_id": c.MustGet("user_id"),
			"role":    c.MustGet("role"),
		})
	})

	// Telegram: link the account to receive order notifications
	protected.GET("/telegram", telegramHandler.Status)
	protected.POST("/telegram/link", telegramHandler.CreateLink)
	protected.DELETE("/telegram/link", telegramHandler.Unlink)

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

	// Recipes: the admin and the cook (the kitchen knows what goes into a dish)
	kitchen := protected.Group("/admin")
	kitchen.Use(handlers.RequireRole("admin", "cook"))
	kitchen.GET("/recipes", recipeHandler.ListRecipes)
	kitchen.GET("/dishes/:id/recipe", recipeHandler.GetRecipe)
	kitchen.PUT("/dishes/:id/recipe", recipeHandler.SaveRecipe)
	kitchen.GET("/ingredients", recipeHandler.ListIngredients)

	admin := protected.Group("/admin")
	admin.Use(handlers.RequireRole("admin"))
	// Ingredient catalogue and purchase prices — admin only
	admin.POST("/ingredients", recipeHandler.CreateIngredient)
	admin.PUT("/ingredients/:id", recipeHandler.UpdateIngredient)
	admin.DELETE("/ingredients/:id", recipeHandler.DeleteIngredient)

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

	return router
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
