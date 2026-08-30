// Package logging creates Alih's structured process logger.
package logging

import (
	"io"
	"log/slog"
)

// New creates a text logger at level that writes to output. Callers own the
// output destination so logs can remain local and be tested without globals.
func New(output io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(output, &slog.HandlerOptions{
		Level: level,
	}))
}
