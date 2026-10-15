package app

import (
	"testing"

	"danny.vn/banhmi/pkg/identity"
)

func TestHostedRejectsAmbiguousDatabaseURIBeforeComposition(t *testing.T) {
	for _, value := range []string{
		"postgres://user:pass@word@db.invalid/db",
		"postgres://user:pass@db%40other.invalid/db",
	} {
		t.Run(value, func(t *testing.T) {
			// No collaborators are supplied. URI validation must precede pool
			// creation and every other composition step.
			hosted := HostedConfig{Identity: identity.Config{AuthDatabaseURL: value}}
			app, err := NewHosted(t.Context(), nil, hosted, nil, "", nil)
			const want = "BANHMI_AUTH_DATABASE_URL: invalid connection URI; percent-encode @ in the password"
			if app != nil || err == nil || err.Error() != want {
				t.Fatalf("NewHosted() = %v, %v, want fixed validation error", app, err)
			}
		})
	}
}
