// Package middleware provides the HTTP middleware chain mounted by
// internal/httpapi. Each function returns a chi-compatible
// `func(http.Handler) http.Handler`.
//
// Order is intentional and lives in cmd/api/main.go; if you change order
// elsewhere, update the comment there too.
package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// requestIDHeader is the canonical header. Lowercase for chi's MIME map.
const requestIDHeader = "X-Request-Id"

// ctxKey is a private type so middleware values can't be read by accident
// from outside this package.
type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota + 1
	ctxKeyLogger
)

// RequestID injects an X-Request-Id header (generated if absent) and
// stores it in the request context. Downstream loggers and handlers
// retrieve it via RequestIDFromContext.
//
// We generate a 16-byte random ID rather than relying on chi's xid-based
// default — easier to grep and free of timestamp leakage.
func RequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimSpace(r.Header.Get(requestIDHeader))
			if id == "" {
				id = newRequestID()
			}
			w.Header().Set(requestIDHeader, id)
			ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequestIDFromContext returns the current request ID, or "" if absent.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok {
		return v
	}
	return ""
}

// LoggerFromContext returns a zerolog.Logger pinned to the current
// request, falling back to the disabled logger if the middleware chain
// isn't installed (eg. in unit tests).
func LoggerFromContext(ctx context.Context) zerolog.Logger {
	if v, ok := ctx.Value(ctxKeyLogger).(zerolog.Logger); ok {
		return v
	}
	return zerolog.Nop()
}

// Logger emits one structured line per request including request_id,
// method, path, status, latency_ms, remote_addr. The body is never logged.
//
// It also pins a child logger into the context so handlers can call
// LoggerFromContext(ctx) and inherit request_id automatically.
func Logger(base zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			id := RequestIDFromContext(r.Context())

			child := base.With().
				Str("request_id", id).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Logger()

			ctx := context.WithValue(r.Context(), ctxKeyLogger, child)

			ww := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r.WithContext(ctx))

			latency := time.Since(start)
			ev := child.Info().
				Int("status", ww.status).
				Dur("latency_ms", latency).
				Str("remote_addr", clientIP(r))
			if ww.bytes > 0 {
				ev = ev.Int64("bytes", ww.bytes)
			}
			ev.Msg("http")
		})
	}
}

// Recoverer catches panics in downstream handlers, logs them, and
// responds with 500. Without this, a panic kills the goroutine and the
// server returns an empty response.
func Recoverer() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log := LoggerFromContext(r.Context())
					log.Error().
						Interface("panic", rec).
						Msg("http: panic recovered")
					http.Error(w, `{"error":{"code":"INTERNAL","message":"internal server error"}}`, http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// MaxBodyBytes limits the request body size at the middleware layer
// before any handler reads it. Larger bodies surface as 413.
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	if limit <= 0 {
		limit = 64 * 1024
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CORS applies a strict allowlist. Empty origins disables cross-origin
// access entirely (the default) — appropriate for a private app whose
// clients run native or are bundled.
func CORS(origins []string) func(http.Handler) http.Handler {
	allow := map[string]struct{}{}
	for _, o := range origins {
		allow[strings.TrimSpace(o)] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if _, ok := allow[origin]; ok {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Vary", "Origin")
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id, Accept, Accept-Encoding")
				}
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// statusRecorder captures status + body length for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is exceedingly rare; degrade to a stable
		// timestamp-based value rather than panicking.
		return time.Now().UTC().Format("20060102T150405.000000")
	}
	return hex.EncodeToString(b[:])
}

func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// X-Forwarded-For can be a comma list; take the first.
		if i := strings.IndexByte(v, ','); i > 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-Ip"); v != "" {
		return v
	}
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return host
}
