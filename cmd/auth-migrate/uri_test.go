package main

import (
	"context"
	"testing"
)

func TestRejectAmbiguousDatabaseURIBeforeConnection(t *testing.T) {
	for _, value := range []string{
		"postgres://user:pass@word@db.invalid/db",
		"postgres://user:pass@db%40other.invalid/db",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(migrationDatabaseURLEnv, value)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			err := run(ctx)
			want := migrationDatabaseURLEnv + ": invalid connection URI; percent-encode @ in the password"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want fixed validation error before database access", err)
			}
		})
	}
}
