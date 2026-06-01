// Package temporalx wires banhmi's Temporal client and worker setup in one place
// so the worker entrypoint, workflows, and activities share consistent options
// and structured logging. It holds no business logic — only SDK plumbing.
package temporalx

import (
	"fmt"
	"log/slog"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"

	"danny.vn/banhmi/pkg/base/config"
)

// Dial connects to the Temporal frontend described by cfg, routing the SDK's logs
// through banhmi's slog handler. The caller owns the returned client and must
// Close it.
func Dial(cfg config.TemporalConfig, logger *slog.Logger) (client.Client, error) {
	c, err := client.Dial(client.Options{
		HostPort:  cfg.HostPort,
		Namespace: cfg.Namespace,
		Logger:    slogAdapter{logger},
	})
	if err != nil {
		return nil, fmt.Errorf("dial temporal %s: %w", cfg.HostPort, err)
	}
	return c, nil
}

// slogAdapter adapts *slog.Logger to the Temporal SDK log.Logger interface. The
// SDK passes alternating key/value pairs as variadic args, which is exactly
// slog's args shape, so each call forwards directly.
type slogAdapter struct{ l *slog.Logger }

var _ log.Logger = slogAdapter{}

func (s slogAdapter) Debug(msg string, kv ...any) { s.l.Debug(msg, kv...) }
func (s slogAdapter) Info(msg string, kv ...any)  { s.l.Info(msg, kv...) }
func (s slogAdapter) Warn(msg string, kv ...any)  { s.l.Warn(msg, kv...) }
func (s slogAdapter) Error(msg string, kv ...any) { s.l.Error(msg, kv...) }
