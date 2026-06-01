package config

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDSNQuote(t *testing.T) {
	cases := map[string]string{
		"simple":     "simple",
		"":           "''",
		"a b":        "'a b'",
		`pa'ss`:      `'pa\'ss'`,
		`back\slash`: `'back\\slash'`,
	}
	for in, want := range cases {
		if got := dsnQuote(in); got != want {
			t.Errorf("dsnQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDSNRoundTrip proves a password with a space, single quote, and backslash
// survives into pgx's parsed config — the bug raw concatenation would corrupt.
func TestDSNRoundTrip(t *testing.T) {
	d := DatabaseConfig{
		Host: "ep-x.aws.neon.tech", Port: 5432, User: "neondb_owner",
		DBName: "neondb", SSLMode: "require", Password: `p@ss w'o\rd`,
	}
	cfg, err := pgxpool.ParseConfig(d.DSN())
	if err != nil {
		t.Fatalf("ParseConfig(%q): %v", d.DSN(), err)
	}
	if cfg.ConnConfig.Password != d.Password {
		t.Errorf("round-trip password = %q, want %q", cfg.ConnConfig.Password, d.Password)
	}
	if cfg.ConnConfig.Host != d.Host || cfg.ConnConfig.Database != d.DBName {
		t.Errorf("host/db mismatch: host=%q db=%q", cfg.ConnConfig.Host, cfg.ConnConfig.Database)
	}
}

func TestEmbedEndpointHost(t *testing.T) {
	cfg := Default()
	if got := cfg.EmbedEndpoint(); got != hostEmbedEndpoint {
		t.Fatalf("host EmbedEndpoint() = %q, want %q", got, hostEmbedEndpoint)
	}
}

func TestEmbeddingEndpointUsesComposeServiceInContainerConfig(t *testing.T) {
	cfg := Default()
	cfg.Database.Host = "postgres"
	if got := cfg.EmbedEndpoint(); got != containerEmbedEndpoint {
		t.Fatalf("container EmbedEndpoint() = %q, want %q", got, containerEmbedEndpoint)
	}
}
