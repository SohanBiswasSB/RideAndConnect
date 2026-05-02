package middleware

import (
	"net/http"
	"sync"
	"time"
)

// RateLimitPerIP is an in-memory token bucket per client IP. It refills
// at `rps` tokens/second up to a burst of `rps`. Suitable for a single
// instance — horizontal scale would need a shared store (Redis).
//
// On exhaustion the response is 429 with a `Retry-After` header rounded
// up to whole seconds. The error envelope matches docs/API.md §1.2.
func RateLimitPerIP(rps int) func(http.Handler) http.Handler {
	if rps <= 0 {
		rps = 20
	}
	rl := newIPLimiter(rps)
	go rl.startSweeper(5 * time.Minute)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !rl.allow(ip) {
				w.Header().Set("Retry-After", "1")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"code":"RATE_LIMITED","message":"too many requests"}}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ipLimiter is a simple O(1) token-bucket map keyed by IP.
type ipLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*ipBucket
}

type ipBucket struct {
	tokens   float64
	lastSeen time.Time
}

func newIPLimiter(rps int) *ipLimiter {
	return &ipLimiter{
		rate:    float64(rps),
		burst:   float64(rps),
		buckets: make(map[string]*ipBucket),
	}
}

func (l *ipLimiter) allow(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		b = &ipBucket{tokens: l.burst, lastSeen: now}
		l.buckets[ip] = b
	}
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens = minFloat(l.burst, b.tokens+elapsed*l.rate)
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// startSweeper drops buckets that haven't been touched in a while so the
// map doesn't grow unbounded for transient clients. Long-lived high-rate
// clients keep their bucket as expected.
func (l *ipLimiter) startSweeper(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for now := range t.C {
		cutoff := now.Add(-2 * interval)
		l.mu.Lock()
		for ip, b := range l.buckets {
			if b.lastSeen.Before(cutoff) {
				delete(l.buckets, ip)
			}
		}
		l.mu.Unlock()
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
