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

// TestOCRLanguagesFollowsJurisdiction characterizes the one-main-language-per-
// country policy: non-VN jurisdictions OCR in their descriptor language; VN (the
// compiled fallback) honors the configured value.
func TestOCRLanguagesFollowsJurisdiction(t *testing.T) {
	c := Default()
	if got := c.OCRLanguages(); got != "vi" {
		t.Errorf("default OCRLanguages = %q, want vi", got)
	}
	c.Extract.OCR.Languages = "vi,en"
	if got := c.OCRLanguages(); got != "vi,en" {
		t.Errorf("vn configured OCRLanguages = %q, want vi,en", got)
	}
	c.Jurisdiction = "my"
	if got := c.OCRLanguages(); got != "en" {
		t.Errorf("my OCRLanguages = %q, want en (policy-locked)", got)
	}
}

// TestDBNameFollowsJurisdiction guards the one-database-per-country default: a
// deployment that selects a jurisdiction but never names a database must land in
// that country's database, never VN's. Explicit env always wins.
func TestDBNameFollowsJurisdiction(t *testing.T) {
	cases := []struct {
		name         string
		jurisdiction string
		dbNameEnv    string
		want         string
	}{
		{"vn default", "vn", "", "banhmi"},
		{"absent jurisdiction", "", "", "banhmi"},
		{"my defaults to laksa", "my", "", "laksa"},
		{"explicit env wins", "my", "banhmi", "banhmi"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BANHMI_JURISDICTION", c.jurisdiction)
			t.Setenv("BANHMI_DATABASE_NAME", c.dbNameEnv)
			cfg, err := Load("testdata/does-not-exist.yaml")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Database.DBName != c.want {
				t.Errorf("DBName = %q, want %q", cfg.Database.DBName, c.want)
			}
		})
	}
}
