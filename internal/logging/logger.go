// Package logging configures the application logger and exposes a
// sanitizer for any user-supplied map that may contain sensitive fields.
//
// The deny list (see Sanitize) is the canonical implementation of
// docs/SECURITY.md §8.2 — keep them in sync.
package logging

import (
	"io"
	"os"
	"strings"

	"github.com/rs/zerolog"
)

// New constructs the application's root logger. In dev the output is a
// human-readable console stream; in staging/prod it's compact JSON.
//
// `level` accepts the zerolog level names (debug, info, warn, error,
// fatal). Unknown values fall back to info and the caller is expected to
// log a warning.
func New(env, level string, out io.Writer) zerolog.Logger {
	if out == nil {
		out = os.Stdout
	}
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs

	parsed, err := zerolog.ParseLevel(strings.ToLower(level))
	if err != nil || parsed == zerolog.NoLevel {
		parsed = zerolog.InfoLevel
	}

	var writer io.Writer = out
	if env == "dev" {
		writer = zerolog.ConsoleWriter{Out: out, TimeFormat: "15:04:05.000"}
	}

	return zerolog.New(writer).Level(parsed).With().
		Timestamp().
		Str("service", "rideandconnect").
		Str("env", env).
		Logger()
}
