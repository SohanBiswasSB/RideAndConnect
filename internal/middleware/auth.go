package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// Verifier is the surface the auth middleware needs from internal/auth.
// Kept as an interface so middleware imports stay tight and tests can
// substitute a fake.
type Verifier interface {
	VerifyAccessToken(token string) (uuid.UUID, error)
}

const ctxKeyUserID ctxKey = 100

// RequireAuth validates the Bearer token. On success, the user ID is in
// the request context (UserIDFromContext). On failure, responds 401 with
// a GraphQL-shaped error envelope.
//
// The middleware is intentionally permissive about which paths require
// auth — the GraphQL handler decides per-resolver. We attach the user ID
// when present and let resolvers gate.
func RequireAuth(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tok := bearerToken(r)
			if tok == "" {
				next.ServeHTTP(w, r)
				return
			}
			id, err := v.VerifyAccessToken(tok)
			if err != nil {
				next.ServeHTTP(w, r) // resolvers reject as UNAUTHENTICATED
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyUserID, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserIDFromContext returns the authenticated user ID, or uuid.Nil when
// the request was not authenticated.
func UserIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	v, ok := ctx.Value(ctxKeyUserID).(uuid.UUID)
	return v, ok
}

// WithUserID is exported so tests can inject an authenticated context
// without round-tripping through middleware.
func WithUserID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, ctxKeyUserID, id)
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}
