package identity

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	t.Setenv("BANHMI_PUBLIC_ORIGIN", "https://evidence.example")
	t.Setenv("BANHMI_AUTH_DATABASE_URL", "postgres://runtime.invalid/auth")
	t.Setenv("BANHMI_GOOGLE_CLIENT_ID", "client-id")
	t.Setenv("BANHMI_GOOGLE_CLIENT_SECRET", "client-secret")
	t.Setenv("BANHMI_AUTH_TRANSACTION_KEY", base64.StdEncoding.EncodeToString(key))

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if got, want := cfg.PublicOrigin.String(), "https://evidence.example"; got != want {
		t.Fatalf("PublicOrigin = %q, want %q", got, want)
	}
	if got, want := string(cfg.TransactionKey[:]), string(key); got != want {
		t.Fatalf("TransactionKey = %q, want %q", got, want)
	}
}

func TestLoadConfigRejectsSecretValueWithoutDisclosingIt(t *testing.T) {
	const secret = "not-base64-secret"
	t.Setenv("BANHMI_PUBLIC_ORIGIN", "https://evidence.example")
	t.Setenv("BANHMI_AUTH_DATABASE_URL", "postgres://runtime.invalid/auth")
	t.Setenv("BANHMI_GOOGLE_CLIENT_ID", "client-id")
	t.Setenv("BANHMI_GOOGLE_CLIENT_SECRET", "client-secret")
	t.Setenv("BANHMI_AUTH_TRANSACTION_KEY", secret)

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want transaction-key error")
	}
	if !strings.Contains(err.Error(), "BANHMI_AUTH_TRANSACTION_KEY") {
		t.Fatalf("error %q does not name the field", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error %q discloses the secret value", err)
	}
}

func TestLoadConfigRejectsNonCanonicalOrigin(t *testing.T) {
	t.Setenv("BANHMI_PUBLIC_ORIGIN", "https://evidence.example/path")
	t.Setenv("BANHMI_AUTH_DATABASE_URL", "postgres://runtime.invalid/auth")
	t.Setenv("BANHMI_GOOGLE_CLIENT_ID", "client-id")
	t.Setenv("BANHMI_GOOGLE_CLIENT_SECRET", "client-secret")
	t.Setenv("BANHMI_AUTH_TRANSACTION_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))

	_, err := LoadConfig()
	if err == nil || !strings.Contains(err.Error(), "BANHMI_PUBLIC_ORIGIN") {
		t.Fatalf("LoadConfig() error = %v, want public-origin error", err)
	}
}

func TestLoadConfigRejectsOriginDelimitersAndInvalidAuthority(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	tests := []string{
		"https://evidence.example?",
		"https://evidence.example#",
		"https://:443",
		"https://evidence.example:",
		"https://evidence.example:99999",
	}
	for _, origin := range tests {
		t.Run(origin, func(t *testing.T) {
			t.Setenv("BANHMI_PUBLIC_ORIGIN", origin)
			t.Setenv("BANHMI_AUTH_DATABASE_URL", "postgres://runtime.invalid/auth")
			t.Setenv("BANHMI_GOOGLE_CLIENT_ID", "client-id")
			t.Setenv("BANHMI_GOOGLE_CLIENT_SECRET", "client-secret")
			t.Setenv("BANHMI_AUTH_TRANSACTION_KEY", key)

			_, err := LoadConfig()
			if err == nil || !strings.Contains(err.Error(), "BANHMI_PUBLIC_ORIGIN") {
				t.Fatalf("LoadConfig() error = %v, want public-origin error", err)
			}
		})
	}
}

