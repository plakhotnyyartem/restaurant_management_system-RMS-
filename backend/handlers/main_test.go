package handlers

import (
	"os"
	"testing"

	"restaurant-management/config"
)

// TestMain loads backend/.env so tests that need the database find DATABASE_URL.
// Without it those tests would skip silently and "ok" would hide that.
func TestMain(m *testing.M) {
	_ = config.Load()
	os.Exit(m.Run())
}
