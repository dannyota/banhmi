package main

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLocalDatabaseURLPasswordRoundTrip(t *testing.T) {
	t.Setenv("PGSERVICE", "")
	for _, password := range []string{"synthetic@password", "synthetic password", "synthetic%password", "synthetic#?/+:password"} {
		t.Run(password, func(t *testing.T) {
			cfg, err := pgxpool.ParseConfig(localDatabaseURL(password))
			if err != nil {
				t.Fatal(err)
			}
			conn := cfg.ConnConfig
			if conn.Password != password {
				t.Fatal("synthetic password did not round-trip")
			}
			if conn.User != "banhmi" || conn.Host != "localhost" || conn.Port != 5432 || conn.Database != "banhmi" {
				t.Fatal("password changed the connection target")
			}
		})
	}
}
