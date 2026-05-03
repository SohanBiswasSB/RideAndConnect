package database

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
)

// pruneInterval is how often the sweeper runs. Hourly is generous —
// nothing in our retention model is so urgent that minute-granularity
// matters.
const pruneInterval = 1 * time.Hour

// pruneTask is one row in the sweeper plan: a query and a label for
// logging.
type pruneTask struct {
	label string
	query string
}

// pruneTasks enumerates the deletes the sweeper performs. Retention
// windows live here as the source of truth; tweak via the constants
// below if needed.
//
//   - ride_routes: kept 7 days past expiry (debugging headroom).
//   - refresh_tokens: kept 7 days past expiry.
//   - ws_tickets: 5-min TTL is short, but consumed/expired rows
//     accumulate; prune them after 1 day.
//   - emergency_events: kept indefinitely (audit value); pruned only
//     when ride_id was set NULL by ride deletion AND > 1 year old.
//   - audit_events: kept 90 days.
var pruneTasks = []pruneTask{
	{"ride_routes",      `DELETE FROM ride_routes      WHERE expires_at < now() - interval '7 days'`},
	{"refresh_tokens",   `DELETE FROM refresh_tokens   WHERE expires_at < now() - interval '7 days'`},
	{"ws_tickets",       `DELETE FROM ws_tickets       WHERE expires_at < now() - interval '1 day'`},
	{"audit_events",     `DELETE FROM audit_events     WHERE occurred_at < now() - interval '90 days'`},
}

// StartPruneSweeper launches a goroutine that runs pruneTasks on a
// ticker. The sweeper exits when ctx is canceled. Best-effort: errors
// are logged at warn level and the next tick retries.
func StartPruneSweeper(ctx context.Context, pool *pgxpool.Pool, log zerolog.Logger) {
	if pool == nil {
		return
	}
	pruneLog := log.With().Str("component", "db_prune").Logger()
	go func() {
		// Run once shortly after startup so a freshly-deployed instance
		// catches up on a backlog without waiting an hour.
		first := time.NewTimer(2 * time.Minute)
		defer first.Stop()
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			runPrune(ctx, pool, pruneLog)
		}
		t := time.NewTicker(pruneInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runPrune(ctx, pool, pruneLog)
			}
		}
	}()
}

func runPrune(ctx context.Context, pool *pgxpool.Pool, log zerolog.Logger) {
	for _, task := range pruneTasks {
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		tag, err := pool.Exec(runCtx, task.query)
		cancel()
		if err != nil {
			log.Warn().Err(err).Str("table", task.label).Msg("prune failed")
			continue
		}
		if n := tag.RowsAffected(); n > 0 {
			log.Info().Str("table", task.label).Int64("rows", n).Msg("pruned")
		}
	}
}
