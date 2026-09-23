package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"restaurant-management/app"
	"restaurant-management/config"
	"restaurant-management/database"
	"restaurant-management/handlers"
	"restaurant-management/telegram"
)

func main() {
	// Settings come from the environment or backend/.env (see .env.example).
	if err := config.Load(); err != nil {
		log.Fatal(err)
	}
	// Gin reads GIN_MODE when its package loads, before .env is read,
	// so the mode from .env is applied explicitly here. "release" hides
	// the route dump and debug warnings — use it when the site is public.
	gin.SetMode(config.Get("GIN_MODE", gin.DebugMode))

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

	// Telegram bot: optional, works only when a token is configured.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var bot *handlers.TelegramBot
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		hour, err := strconv.Atoi(config.Get("DIGEST_HOUR", "9"))
		if err != nil || hour < 0 || hour > 23 {
			log.Fatal("DIGEST_HOUR must be a number from 0 to 23")
		}
		bot = handlers.NewTelegramBot(db, telegram.NewClient(token, os.Getenv("TELEGRAM_API_URL")), hour)
		if err := bot.Init(ctx); err != nil {
			log.Printf("Telegram bot is disabled: %v", err)
			bot = nil
		} else {
			log.Printf("Telegram bot started: https://t.me/%s", bot.Username)
			go bot.Run(ctx)
		}
	} else {
		log.Println("Telegram bot is off (TELEGRAM_BOT_TOKEN is not set)")
	}

	router := app.NewRouter(db, bot)

	port := config.Get("PORT", "8080")
	server := &http.Server{Addr: ":" + port, Handler: router}

	// NotifyContext takes over Ctrl+C and SIGTERM, so the process no longer
	// exits by itself: on the signal we stop the web server explicitly,
	// letting requests that are in progress finish.
	go func() {
		<-ctx.Done()
		log.Println("Shutting down…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
	}()

	log.Println("Server started on http://localhost:" + port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	if bot != nil {
		bot.Wait() // let queued Telegram notifications go out
	}
	log.Println("Server stopped")
}
