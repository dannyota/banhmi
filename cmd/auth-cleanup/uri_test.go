package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRejectAmbiguousDatabaseURIBeforeConnection(t *testing.T) {
	for _, value := range []string{
		"postgres://user:pass@word@db.invalid/db",
		"postgres://user:pass@db%40other.invalid/db",
		"postgres://db.invalid?password=pass@word",
		"postgresql://db.invalid?password=pass@word",
		"postgres://user:pass?part@word@db.invalid/db",
		"postgresql://user:pass?part@word@db.invalid/db",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(migrationDatabaseURLEnv, value)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := run(ctx, 1, time.Time{})
			want := migrationDatabaseURLEnv + ": invalid connection URI; percent-encode @, ?, and # in the user and password"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want fixed validation error before database access", err)
			}
			var output bytes.Buffer
			slog.New(slog.NewTextHandler(&output, nil)).Error("auth cleanup failed", "err", err)
			for _, fragment := range []string{value, "db.invalid", "pass@word", "pass?part", "user:"} {
				if strings.Contains(output.String(), fragment) {
					t.Fatal("logged error contains connection content")
				}
			}
		})
	}
}
