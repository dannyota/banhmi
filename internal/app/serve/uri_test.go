package serve

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/identity"
)

func TestHostedRejectsAmbiguousDatabaseURIBeforeComposition(t *testing.T) {
	for _, value := range []string{
		"postgres://user:pass@word@db.invalid/db",
		"postgres://user:pass@db%40other.invalid/db",
		"postgres://db.invalid?password=pass@word",
		"postgresql://db.invalid?password=pass@word",
		"postgres://user:pass?part@word@db.invalid/db",
		"postgresql://user:pass?part@word@db.invalid/db",
	} {
		t.Run(value, func(t *testing.T) {
			// No collaborators are supplied. URI validation must precede pool
			// creation and every other composition step.
			hosted := HostedConfig{Identity: identity.Config{AuthDatabaseURL: value}}
			app, err := NewHosted(t.Context(), nil, hosted, nil, "", nil)
			const want = "BANHMI_AUTH_DATABASE_URL: invalid connection URI; percent-encode @, ?, and # in the user and password"
			if app != nil || err == nil || err.Error() != want {
				t.Fatalf("NewHosted() = %v, %v, want fixed validation error", app, err)
			}
			var output bytes.Buffer
			fmt.Fprint(&output, err)
			for _, fragment := range []string{value, "db.invalid", "pass@word", "pass?part", "user:"} {
				if strings.Contains(output.String(), fragment) {
					t.Fatal("connection error output contains connection content")
				}
			}
		})
	}
}
