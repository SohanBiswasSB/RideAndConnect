package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

// GzipRequestBody decompresses inbound bodies with Content-Encoding:
// gzip before handing them to downstream handlers. Saves ~40% on POST
// payloads when the client knows it can compress.
//
// Decompression-bomb defense: the *decompressed* stream is bounded by
// decompressedLimit. MaxBodyBytes upstream caps the *compressed* wire
// bytes; this layer caps the post-decompression bytes independently.
//
// Headers are rewritten so handlers don't see Content-Encoding /
// Content-Length anymore (both are post-decompression irrelevant).
func GzipRequestBody() func(http.Handler) http.Handler {
	return GzipRequestBodyWithLimit(decompressedLimitDefault)
}

const decompressedLimitDefault = 256 * 1024

// GzipRequestBodyWithLimit lets callers tune the post-decompression
// body cap. Pass 0 to keep the default.
func GzipRequestBodyWithLimit(limit int64) func(http.Handler) http.Handler {
	if limit <= 0 {
		limit = decompressedLimitDefault
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.EqualFold(r.Header.Get("Content-Encoding"), "gzip") {
				next.ServeHTTP(w, r)
				return
			}
			if r.Body == nil {
				next.ServeHTTP(w, r)
				return
			}
			gz, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, "bad gzip body", http.StatusBadRequest)
				return
			}
			capped := http.MaxBytesReader(w, gz, limit)
			r.Body = struct {
				io.Reader
				io.Closer
			}{Reader: capped, Closer: closerFn(func() error {
				_ = gz.Close()
				return capped.Close()
			})}
			r.Header.Del("Content-Encoding")
			r.Header.Del("Content-Length")
			r.ContentLength = -1
			next.ServeHTTP(w, r)
		})
	}
}

type closerFn func() error

func (f closerFn) Close() error { return f() }
