package graph

import (
	_ "embed"
	"net/http"
	"time"

	"github.com/graph-gophers/graphql-go"
	"github.com/graph-gophers/graphql-go/relay"
)

// schemaSDL embeds the canonical GraphQL SDL at build time so any
// drift between docs and code is a compile error rather than a
// runtime mystery.
//
//go:embed schema.graphqls
var schemaSDL string

// timeNow is package-level so tests can stub the clock.
var timeNow = func() time.Time { return time.Now() }

// NewHandler builds a graph-gophers/graphql-go handler bound to the
// supplied resolver. introspect=false disables the standard
// introspection schema (per docs/SECURITY.md §16 — disabled in prod).
func NewHandler(resolver *Resolver, introspect bool) (http.Handler, error) {
	opts := []graphql.SchemaOpt{graphql.UseFieldResolvers(), graphql.MaxDepth(8)}
	if !introspect {
		opts = append(opts, graphql.DisableIntrospection())
	}
	schema, err := graphql.ParseSchema(schemaSDL, resolver, opts...)
	if err != nil {
		return nil, err
	}
	return &recoverHandler{inner: &relay.Handler{Schema: schema}}, nil
}

// recoverHandler converts any panic in the resolver layer (e.g. nil
// service in dev mode) into a stable GraphQL error envelope. Without
// this, panics surface as raw stack traces in the response body.
type recoverHandler struct{ inner http.Handler }

func (h *recoverHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"internal error","extensions":{"code":"INTERNAL"}}]}`))
		}
	}()
	h.inner.ServeHTTP(w, r)
}
