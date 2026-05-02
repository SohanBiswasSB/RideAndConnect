package rides

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

// throttleStore is the in-memory token-bucket registry used by the route
// resolvers. Buckets are created lazily and swept periodically.
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

func newThrottle(burst int, refillPerSec float64) *throttleStore {
	return &throttleStore{
		buckets: make(map[string]*tokenBucket),
		rate:    refillPerSec,
		burst:   float64(burst),
		now:     time.Now,
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
