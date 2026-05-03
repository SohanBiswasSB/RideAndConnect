package realtime

import (
	"sync"
	"time"
)

// tokenBucket is the per-connection inbound rate limiter for
// location.update. Refill rate and capacity retune live as the tier
// changes (so a connection that drops to `low` immediately starts
// shedding excess publishes server-side without having to tell the
// client).
//
// Excess publishes are silently dropped — the protocol's documented
// behaviour says we may close on sustained violations, but for inbound
// location specifically we prefer to drop bytes rather than spend more
// bytes on an `error` response on a tight link.
type tokenBucket struct {
	mu      sync.Mutex
	tokens  float64
	capF    float64
	rate    float64 // tokens per second
	last    time.Time
}

// newTokenBucket starts full so the first burst of publishes succeeds.
func newTokenBucket(cap int, refillPerSec float64) *tokenBucket {
	return &tokenBucket{
		tokens: float64(cap),
		capF:   float64(cap),
		rate:   refillPerSec,
		last:   time.Now(),
	}
}

// allow consumes one token. Returns false when the bucket is empty —
// caller drops the inbound message.
func (t *tokenBucket) allow() bool {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if elapsed := now.Sub(t.last).Seconds(); elapsed > 0 {
		t.tokens += elapsed * t.rate
		if t.tokens > t.capF {
			t.tokens = t.capF
		}
		t.last = now
	}
	if t.tokens < 1 {
		return false
	}
	t.tokens--
	return true
}

// retune adjusts capacity and refill rate (called on tier change).
// Holds the existing token count clamped to the new cap so we don't
// suddenly over-grant after a tier drop.
func (t *tokenBucket) retune(cap int, refillPerSec float64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.capF = float64(cap)
	t.rate = refillPerSec
	if t.tokens > t.capF {
		t.tokens = t.capF
	}
}
