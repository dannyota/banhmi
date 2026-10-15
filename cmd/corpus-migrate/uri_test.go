package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
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
			var output bytes.Buffer
			err := run(ctx, []string{"-chain", "writer"}, &output)
			want := migrationDatabaseURLEnv + ": invalid connection URI; percent-encode @, ?, and # in the user and password"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want fixed validation error before database access", err)
			}
			if output.Len() != 0 {
				t.Fatal("migration produced output for rejected configuration")
			}
			slog.New(slog.NewTextHandler(&output, nil)).Error("corpus migration failed", "err", err)
			for _, fragment := range []string{value, "db.invalid", "pass@word", "pass?part", "user:"} {
				if strings.Contains(output.String(), fragment) {
					t.Fatal("logged error contains connection content")
				}
			}
		})
	}
}
