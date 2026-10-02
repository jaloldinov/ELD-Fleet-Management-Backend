// Package logger builds the application's structured logger.
package logger

import (
	"io"
	"log/slog"
)

// New returns a structured logger that writes to w.
//
// In production it writes JSON (easy for log systems like Loki/ELK/CloudWatch
// to parse). In development it writes human-readable text.
//
// w is a parameter (instead of always os.Stdout) so tests can capture output.
func New(w io.Writer, production bool, level string) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var h slog.Handler
	if production {
		h = slog.NewJSONHandler(w, opts)
	} else {
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

func parseLevel(level string) slog.Level {
	var l slog.Level
	// UnmarshalText understands "debug", "info", "warn", "error".
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return slog.LevelInfo
	}
	return l
}
