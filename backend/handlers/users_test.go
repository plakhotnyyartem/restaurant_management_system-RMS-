package handlers

import (
	"context"
	"errors"
	"testing"

	"restaurant-management/database"
)

// The last admin cannot be demoted, even by an admin whose token is older
// than their own demotion. Runs in a transaction that is rolled back.
func TestLastAdminStays(t *testing.T) {
	db, err := database.Connect()
	if err != nil {
		t.Skip("database is not available: ", err)
	}
	defer db.Close()
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `UPDATE users SET role = 'customer' WHERE role = 'admin'`); err != nil {
		t.Fatal(err)
	}
	var id int
	if err := tx.QueryRow(ctx, `UPDATE users SET role = 'admin' WHERE id = (SELECT MIN(id) FROM users) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var user UserRow
	if err := setRole(ctx, tx, id, "customer", &user); !errors.Is(err, errLastAdmin) {
		t.Fatalf("demote last admin: err = %v", err)
	}
	if err := setRole(ctx, tx, id, "admin", &user); err != nil || user.Role != "admin" {
		t.Fatalf("admin stays admin: err = %v, role = %s", err, user.Role)
	}
}
