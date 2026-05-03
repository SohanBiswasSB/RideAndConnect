//go:build integration

package rides_test

// Concurrency invariant: even when N goroutines race to Join the same
// ride simultaneously, exactly MaxMembers active members exist after.
// This test exercises the SELECT … FOR UPDATE + count check + INSERT
// path in rides.Service.Join under contention.
//
// Requires DATABASE_URL to point at a freshly-migrated database. Run:
//
//	docker compose up -d postgres
//	make migrate-up
//	go test -tags=integration -race -run TestJoin_ThreeCapUnderContention ./internal/rides

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SohanBiswasSB/rideandconnect/internal/auth"
	"github.com/SohanBiswasSB/rideandconnect/internal/rides"
)

func TestJoin_ThreeCapUnderContention(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// Owner creates the ride; 5 prospective joiners race.
	const joiners = 5

	owner := mustCreateUser(t, ctx, pool, "Cap-Test Owner")
	users := make([]uuid.UUID, joiners)
	for i := range users {
		users[i] = mustCreateUser(t, ctx, pool, "Cap-Test Joiner")
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, append(append([]uuid.UUID{}, owner), users...))
	}()

	svc := rides.NewService(pool)
	ride, err := svc.Create(ctx, owner, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM rides WHERE id = $1`, ride.ID) })

	// Fire all joins simultaneously.
	var wg sync.WaitGroup
	var successes, fullErrors int32
	start := make(chan struct{})
	for _, uid := range users {
		uid := uid
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Join(ctx, ride.ID, uid, ride.InviteCode)
			switch {
			case err == nil:
				atomic.AddInt32(&successes, 1)
			case err == rides.ErrRideFull:
				atomic.AddInt32(&fullErrors, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	// Owner is already member 1; the cap is 3, so exactly 2 joiners
	// should succeed and 3 should see ErrRideFull.
	if got := int(successes); got != rides.MaxMembers-1 {
		t.Errorf("successful joins = %d, want %d", got, rides.MaxMembers-1)
	}
	if got := int(fullErrors); got != joiners-(rides.MaxMembers-1) {
		t.Errorf("RIDE_FULL errors = %d, want %d", got, joiners-(rides.MaxMembers-1))
	}

	// Sanity: DB confirms exactly MaxMembers active rows.
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM ride_members WHERE ride_id = $1 AND left_at IS NULL
	`, ride.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != rides.MaxMembers {
		t.Errorf("db active members = %d, want %d", n, rides.MaxMembers)
	}
}

// mustCreateUser inserts a minimal user row and returns the id.
func mustCreateUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	hash, err := auth.HashPassword("test-password-12chars")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	phone := uuid.NewString()[:12]
	var id uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO users (name, phone, password_hash) VALUES ($1, $2, $3) RETURNING id
	`, name, phone, hash).Scan(&id)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}
