package app

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/base/config"
)

func TestLoadHostedConfigRejectsMissingIdentityConfigBeforeComposition(t *testing.T) {
	for _, name := range []string{
		"BANHMI_PUBLIC_ORIGIN",
		"BANHMI_AUTH_DATABASE_URL",
		"BANHMI_GOOGLE_CLIENT_ID",
		"BANHMI_GOOGLE_CLIENT_SECRET",
		"BANHMI_AUTH_TRANSACTION_KEY",
		"BANHMI_OAUTH_HMAC_KEY",
		"BANHMI_OAUTH_ROTATED_HMAC_KEYS",
		"BANHMI_OAUTH_CLIENTS_FILE",
	} {
		t.Setenv(name, "")
	}
	_, err := LoadHostedConfig()
	if err == nil || !strings.Contains(err.Error(), "BANHMI_PUBLIC_ORIGIN") {
		t.Fatalf("LoadHostedConfig() error = %v, want missing public origin", err)
	}
}

func TestLoadHostedConfigAcceptsEmptyVersionedClientAllowlist(t *testing.T) {
	clientsFile := filepath.Join(t.TempDir(), "clients.json")
	if err := os.WriteFile(clientsFile, []byte(`{"version":1,"clients":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	t.Setenv("BANHMI_PUBLIC_ORIGIN", "https://evidence.example")
	t.Setenv("BANHMI_AUTH_DATABASE_URL", "postgres://runtime@auth.invalid/auth")
	t.Setenv("BANHMI_GOOGLE_CLIENT_ID", "client-id")
	t.Setenv("BANHMI_GOOGLE_CLIENT_SECRET", "client-secret")
	t.Setenv("BANHMI_AUTH_TRANSACTION_KEY", key)
	t.Setenv("BANHMI_OAUTH_HMAC_KEY", key)
	t.Setenv("BANHMI_OAUTH_ROTATED_HMAC_KEYS", "")
	t.Setenv("BANHMI_OAUTH_CLIENTS_FILE", clientsFile)

	cfg, err := LoadHostedConfig()
	if err != nil {
		t.Fatalf("LoadHostedConfig() error = %v", err)
	}
	if cfg.Identity.PublicOrigin.String() != "https://evidence.example" {
		t.Fatalf("origin = %q", cfg.Identity.PublicOrigin)
	}
}

func TestVietnamConfigAlwaysSelectsVietnam(t *testing.T) {
	t.Setenv("BANHMI_DATABASE_NAME_VN", "")
	base := config.Default()
	base.Jurisdiction = "my"
	base.Database.DBName = "laksa"

	got := vietnamConfig(base)
	if got.Jurisdiction != "vn" || got.Database.DBName != "banhmi" {
		t.Fatalf("vietnamConfig() jurisdiction=%q database=%q", got.Jurisdiction, got.Database.DBName)
	}
	if got.Retrieve.HNSWCandidateMultiplier != -1 {
		t.Fatalf("Vietnam retrieval multiplier = %d, want -1", got.Retrieve.HNSWCandidateMultiplier)
	}
}
