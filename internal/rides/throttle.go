package rides

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// throttleStore is the in-memory token-bucket registry used by the route
// resolvers. Buckets are created lazily and swept periodically so the
// map doesn't grow unbounded for transient (caller, peer) pairs.
//
// This is a single-instance implementation; horizontal scale would back
// it with Redis (deferred per docs/ARCHITECTURE.md §9 v2 plan).
type throttleStore struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64
	burst   float64
	now     func() time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// sweepInterval is how often the background sweeper prunes idle
// buckets. A bucket is considered idle when its `last` timestamp is
// older than (sweepInterval × 2).
const sweepInterval = 10 * time.Minute

// newThrottle constructs a token-bucket store and starts a sweeper
// goroutine bound to ctx. The sweeper terminates when ctx is canceled —
// pass the application root context.
func newThrottle(ctx context.Context, burst int, refillPerSec float64) *throttleStore {
	t := &throttleStore{
		buckets: make(map[string]*tokenBucket),
		rate:    refillPerSec,
		burst:   float64(burst),
		now:     time.Now,
	}
	go t.sweep(ctx)
	return t
}

// sweep removes buckets that haven't been touched recently. Long-lived
// high-rate clients keep their bucket as expected because every allow()
// updates `last`.
func (t *throttleStore) sweep(ctx context.Context) {
	tick := time.NewTicker(sweepInterval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			cutoff := now.Add(-2 * sweepInterval)
			t.mu.Lock()
			for k, b := range t.buckets {
				if b.last.Before(cutoff) {
					delete(t.buckets, k)
				}
			}
			t.mu.Unlock()
		}
	}
}

// allow consumes a token if available; returns the wait duration when
// the bucket is empty.
func (t *throttleStore) allow(key string) (bool, time.Duration) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.buckets[key]
	if !ok {
		b = &tokenBucket{tokens: t.burst, last: now}
		t.buckets[key] = b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * t.rate
		if b.tokens > t.burst {
			b.tokens = t.burst
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	// How long until 1 token is available.
	wait := time.Duration((1-b.tokens)/t.rate*float64(time.Second)) + time.Millisecond
	return false, wait
}

// throttleKey is a small helper for the per-user-per-ride key.
func throttleKey(parts ...uuid.UUID) string {
	out := make([]byte, 0, len(parts)*37)
	for i, p := range parts {
		if i > 0 {
			out = append(out, '|')
		}
		out = append(out, p.String()...)
	}
	return string(out)
}
