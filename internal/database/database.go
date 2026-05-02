// Package database wraps pgxpool with our application's lifecycle and
// observability concerns. Domain packages depend on the *pgxpool.Pool
// directly via this package's Pool type alias.
package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool is a thin alias so call sites depend on internal/database rather
// than reaching into pgx directly. Swap-out friendly.
type Pool = pgxpool.Pool

// ErrNotConfigured is returned by Open when DATABASE_URL is empty. It
// allows main.go to keep the API healthy in dev while the DB is offline.
var ErrNotConfigured = errors.New("database: DATABASE_URL is empty")

// Open parses the DSN, builds a pool with sensible defaults, and verifies
// connectivity with a short ping.
func Open(ctx context.Context, dsn string) (*Pool, error) {
	if dsn == "" {
		return nil, ErrNotConfigured
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("database: parse dsn: %w", err)
	}
	cfg.MaxConns = 10
	cfg.MinConns = 1
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.MaxConnLifetime = 1 * time.Hour
	cfg.HealthCheckPeriod = 30 * time.Second

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("database: connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database: ping: %w", err)
	}
	return pool, nil
}
