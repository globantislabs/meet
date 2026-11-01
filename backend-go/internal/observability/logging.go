// Package observability wires structured logging and Prometheus metrics.
//
// LOGGING RULES (enforced by convention in this codebase):
//   - Never log raw audio, transcripts, or credential material.
//   - Never use personal names as metric labels or log fields; use the
//     server-issued participant/room/session IDs for correlation.
package observability

import (
	"io"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
)

// NewLogger builds the process logger. Console (pretty) output is intended
// for local development; JSON output is intended for log shippers.
func NewLogger(level, format string) zerolog.Logger {
	var w io.Writer = os.Stdout
	out := zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}

	lvl := parseLevel(level)
	zerolog.SetGlobalLevel(lvl)

	if strings.EqualFold(format, "json") {
		return zerolog.New(w).With().Timestamp().Logger()
	}
	return zerolog.New(out).With().Timestamp().Logger()
}

func parseLevel(s string) zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "trace":
		return zerolog.TraceLevel
	default:
		return zerolog.InfoLevel
	}
}
