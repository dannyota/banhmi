package serve

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRawURIConnectionBoundary(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	for i, cause := range connectionCauses() {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Run("creation", func(t *testing.T) {
				pool, err := openDatabasePool(t.Context(), "synthetic URI", func(context.Context, string) (*pgxpool.Pool, error) {
					return nil, cause
				})
				if pool != nil {
					t.Fatal("failed boundary returned a pool")
				}
				assertSafeConnectionFailure(t, err, cause)
			})
			t.Run("first acquire and ping", func(t *testing.T) {
				var attempted atomic.Bool
				open := func(ctx context.Context, _ string) (*pgxpool.Pool, error) {
					cfg, err := pgxpool.ParseConfig("postgres://localhost/banhmi?sslmode=disable")
					if err != nil {
						return nil, err
					}
					cfg.MinConns = 0
					cfg.BeforeConnect = func(context.Context, *pgx.ConnConfig) error {
						attempted.Store(true)
						return cause
					}
					return pgxpool.NewWithConfig(ctx, cfg)
				}
				ctx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				pool, err := openDatabasePool(ctx, "synthetic URI", open)
				if pool != nil || !attempted.Load() {
					t.Fatal("first connection failure was not injected")
				}
				assertSafeConnectionFailure(t, err, cause)
			})
		})
	}
}

func assertSafeConnectionFailure(t *testing.T, err, cause error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected connection failure")
	}
	var output bytes.Buffer
	slog.New(slog.NewTextHandler(&output, nil)).Error("operation failed", "err", err)
	for _, text := range []string{err.Error(), output.String(), fmt.Sprintf("%+v", err)} {
		if strings.Contains(text, "synthetic@tail") {
			t.Fatal("credential fragment leaked")
		}
		if !strings.Contains(text, "connect auth database: connection failed") {
			t.Fatal("connection classification missing")
		}
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, sentinel) != errors.Is(cause, sentinel) {
			t.Fatal("context classification changed")
		}
	}
	var original, safe *pgconn.PgError
	if errors.As(cause, &original) && original.Code == "28P01" {
		if !errors.As(err, &safe) || safe.Code != original.Code || safe.Message != "" || safe.Detail != "" {
			t.Fatal("SQLSTATE must survive without message or detail")
		}
		if !strings.Contains(output.String(), "28P01") {
			t.Fatal("SQLSTATE missing from output")
		}
	}
}

func connectionCauses() []error {
	fragment := errors.New("lookup synthetic@tail: connection refused")
	return []error{
		fragment,
		fmt.Errorf("synthetic@tail: %w", context.Canceled),
		fmt.Errorf("synthetic@tail: %w", context.DeadlineExceeded),
		fmt.Errorf("synthetic@tail: %w", errors.Join(context.Canceled, context.DeadlineExceeded)),
		&pgconn.PgError{Code: "28P01", Message: fragment.Error(), Detail: fragment.Error()},
		&pgconn.PgError{Code: "synthetic@tail", Message: fragment.Error()},
	}
}
