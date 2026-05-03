//go:build integration

package users_test

// Pins the pgx scan behavior for numeric(4,1) → *float64. A future
// driver upgrade that changes the default scanner for numeric would
// surface as a fail here rather than as silently-wrong tyre pressures
// in production.
//
// Requires DATABASE_URL with the schema migrated up.

import (
	"context"
	"math"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestUsers_TyrePressureNumericRoundtrip(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	const want = 33.5

	var id uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (name, phone, password_hash, tyre_front_solo_psi)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, "TyreTest", uuid.NewString()[:12], "hash", want).Scan(&id); err != nil {
		t.Fatalf("insert: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, id) })

	var got *float64
	if err := pool.QueryRow(ctx, `SELECT tyre_front_solo_psi FROM users WHERE id = $1`, id).Scan(&got); err != nil {
		t.Fatalf("select: %v", err)
	}
	if got == nil {
		t.Fatalf("scanned nil; expected %v", want)
	}
	if diff := math.Abs(*got - want); diff > 0.05 {
		t.Errorf("got %v, want %v (diff %v)", *got, want, diff)
	}
}
