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

	router.GET("/api/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":   "ok",
			"database": "connected",
		})
	})

	router.POST("/api/auth/register", authHandler.Register)
	router.POST("/api/auth/login", authHandler.Login)
	router.GET("/api/categories", categoryHandler.GetCategories)

	protected := router.Group("/api")
	protected.Use(handlers.AuthMiddleware())

	protected.GET("/profile", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "you are authorized",
			"user_id": c.MustGet("user_id"),
			"role":    c.MustGet("role"),
		})
	})

	admin := protected.Group("/admin")
	admin.Use(handlers.RequireRole("admin"))
	admin.POST("/categories", categoryHandler.CreateCategory)
	admin.PUT("/categories/:id", categoryHandler.UpdateCategory)
	admin.DELETE("/categories/:id", categoryHandler.DeleteCategory)

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
