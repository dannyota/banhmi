// Package log configures banhmi's structured logger (log/slog).
package log

import (
	"log/slog"
	"os"
	"strings"
)

// New returns a slog.Logger writing to stderr at the given level
// ("debug", "info", "warn", "error"; anything else means info).
func New(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}
