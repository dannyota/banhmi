package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDefaultReadLimits(t *testing.T) {
	got := Default().Retrieve.ReadLimits
	want := ReadLimits{MaxQueryBytes: 4096, MaxResults: 50, MaxRelated: 50, MaxFilterValues: 20, MaxFilterBytes: 256, MaxDocumentChunks: 50}
	if got != want {
		t.Fatalf("read limits = %+v, want %+v", got, want)
	}
}

func TestLoadRejectsExplicitInvalidReadLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("retrieve:\n  max_results: 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted max_results: 0")
	}
}

func TestLoadReadLimitEnvironmentOverride(t *testing.T) {
	t.Setenv("BANHMI_RETRIEVE_MAX_QUERY_BYTES", "1234")
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retrieve.MaxQueryBytes != 1234 {
		t.Fatalf("MaxQueryBytes = %d, want 1234", cfg.Retrieve.MaxQueryBytes)
	}
}

func TestLoadRejectsInvalidReadLimitEnvironment(t *testing.T) {
	for _, value := range []string{"0", "-1", "many"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BANHMI_RETRIEVE_MAX_RESULTS", value)
			if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
				t.Fatalf("Load accepted BANHMI_RETRIEVE_MAX_RESULTS=%q", value)
			}
		})
	}
}

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

// The pipeline container ships no config file, so the Kaggle embed engine must
// be fully usable from Default() alone — an empty ModelDataset pushes a kernel
// with no model attached and the whole embed pass fails.
func TestDefaultKaggleEmbedComplete(t *testing.T) {
	k := Default().Embed.Kaggle
	if k.ModelDataset == "" {
		t.Fatal("Default() Embed.Kaggle.ModelDataset is empty")
	}
	if k.Accelerator == "" {
		t.Fatal("Default() Embed.Kaggle.Accelerator is empty")
	}
	if k.MinBatch <= 0 {
		t.Fatalf("Default() Embed.Kaggle.MinBatch = %d, want > 0", k.MinBatch)
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

func TestStorageDirFollowsJurisdiction(t *testing.T) {
	cases := []struct {
		name         string
		jurisdiction string
		storageEnv   string
		want         string
	}{
		{"vn default", "vn", "", "data/vn"},
		{"my default", "my", "", "data/my"},
		{"id default", "id", "", "data/id"},
		{"explicit env wins", "my", "/custom/path", "/custom/path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("BANHMI_JURISDICTION", c.jurisdiction)
			t.Setenv("BANHMI_STORAGE_DIR", c.storageEnv)
			t.Setenv("BANHMI_DATABASE_NAME", "x")
			cfg, err := Load("testdata/does-not-exist.yaml")
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Storage.Dir != c.want {
				t.Errorf("Storage.Dir = %q, want %q", cfg.Storage.Dir, c.want)
			}
		})
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
