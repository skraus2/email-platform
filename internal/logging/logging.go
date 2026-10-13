// Package logging builds the structured slog logger used by all processes.
//
// Rule: never log secrets (API keys, tokens, DATABASE_URL), email bodies or
// unmasked recipient addresses.
package logging

import (
	"io"
	"log/slog"
)

// New returns a logger writing to w in the given format ("json" or "text"),
// tagged with the process name so API and worker logs are distinguishable.
func New(w io.Writer, format string, level slog.Level, process string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h).With(slog.String("process", process))
}
