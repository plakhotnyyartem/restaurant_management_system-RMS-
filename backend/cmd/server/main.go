package main

import (
	"log"

	"restaurant-management/app"
	"restaurant-management/config"
	"restaurant-management/database"
	"restaurant-management/handlers"
)

func main() {
	// Settings come from the environment or backend/.env (see .env.example).
	if err := config.Load(); err != nil {
		log.Fatal(err)
	}
	secret, err := config.Required("JWT_SECRET")
	if err != nil {
		log.Fatal(err)
	}
	if err := handlers.InitJWT(secret); err != nil {
		log.Fatal(err)
	}

	db, err := database.Connect()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	router := app.NewRouter(db)

	port := config.Get("PORT", "8080")
	log.Println("Server started on http://localhost:" + port)

	if err := router.Run(":" + port); err != nil {
		log.Fatal(err)
	}
}
