package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"restaurant-management/config"
)

// Connect opens a connection pool using DATABASE_URL, e.g.
// postgres://user:password@localhost:5432/restaurant_management
func Connect() (*pgxpool.Pool, error) {
	dsn, err := config.Required("DATABASE_URL")
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("database connection error: %w", err)
	}

	if err := pool.Ping(context.Background()); err != nil {
		return nil, fmt.Errorf("database ping error: %w", err)
	}

	return pool, nil
}