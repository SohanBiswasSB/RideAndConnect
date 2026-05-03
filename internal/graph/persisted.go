package graph

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
)

// PersistedQueryMiddleware implements the Apollo Persisted Queries
// (APQ) protocol. Clients send only a SHA-256 hash on subsequent
// requests; the server caches the inline query the first time it sees
// the (hash, query) pair.
//
// Wire savings: a typical GraphQL query is 200–500 bytes of text;
// replacing it with a 64-character hash inside an extensions object is
// ~140 bytes total. Multiplied by every retry on a flaky cellular
// link, this adds up.
//
// Protocol (Apollo APQ):
//
//	{ "query": null,
//	  "extensions": {
//	    "persistedQuery": { "version": 1, "sha256Hash": "..." }
//	  } }
//
// On miss, server returns:
//
//	{ "errors": [{
//	    "message": "PersistedQueryNotFound",
//	    "extensions": { "code": "PERSISTED_QUERY_NOT_FOUND" } }] }
//
// Client retries with the full `query` field; server caches it under
// the hash and dispatches normally.
//
// The cache is in-memory (sync.Map) and unbounded — fine for our
// expected client query set (~20 unique queries). For a public API
// you'd want an LRU.
func PersistedQueryMiddleware() func(http.Handler) http.Handler {
	cache := &persistedCache{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "read body", http.StatusBadRequest)
				return
			}
			_ = r.Body.Close()

			var req persistedRequest
			if err := json.Unmarshal(body, &req); err != nil {
				// Not JSON we recognise — pass through (lets non-APQ
				// clients work unchanged).
				r.Body = io.NopCloser(bytes.NewReader(body))
				next.ServeHTTP(w, r)
				return
			}

			hash := req.persistedHash()
			if hash != "" {
				if req.Query == "" {
					// Hash-only request: look up.
					q, ok := cache.get(hash)
					if !ok {
						writePQNotFound(w)
						return
					}
					req.Query = q
				} else {
					// Both supplied: verify the hash matches the query
					// (defense against a client lying about the hash to
					// poison the cache for someone else).
					if computedHash(req.Query) != hash {
						http.Error(w, "persisted query hash mismatch", http.StatusBadRequest)
						return
					}
					cache.set(hash, req.Query)
				}
				// Re-marshal with the resolved query so downstream
				// gqlgen handler sees a normal request.
				rewritten, err := json.Marshal(req)
				if err != nil {
					http.Error(w, "internal", http.StatusInternalServerError)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(rewritten))
				r.ContentLength = int64(len(rewritten))
			} else {
				// No persisted-query extension; restore body and pass through.
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			next.ServeHTTP(w, r)
		})
	}
}

type persistedRequest struct {
	Query         string         `json:"query"`
	OperationName string         `json:"operationName,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
	Extensions    map[string]any `json:"extensions,omitempty"`
}

func (r persistedRequest) persistedHash() string {
	if r.Extensions == nil {
		return ""
	}
	pq, ok := r.Extensions["persistedQuery"].(map[string]any)
	if !ok {
		return ""
	}
	h, _ := pq["sha256Hash"].(string)
	return h
}

func computedHash(query string) string {
	sum := sha256.Sum256([]byte(query))
	return hex.EncodeToString(sum[:])
}

type persistedCache struct {
	m sync.Map // hash → query
}

func (c *persistedCache) get(h string) (string, bool) {
	v, ok := c.m.Load(h)
	if !ok {
		return "", false
	}
	return v.(string), true
}

func (c *persistedCache) set(h, q string) { c.m.Store(h, q) }

var errPQNotFound = errors.New("persisted query not found")

func writePQNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"errors":[{"message":"PersistedQueryNotFound","extensions":{"code":"PERSISTED_QUERY_NOT_FOUND"}}]}`))
}

// Compile-time use of errPQNotFound to keep golangci's unused linters
// happy if they ever fire — it's referenced in tests and future error
// paths.
var _ = errPQNotFound
