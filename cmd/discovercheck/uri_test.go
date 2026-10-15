package main

import "testing"

func TestConfiguredDatabaseURLRejectsAmbiguousURIBeforeDiscovery(t *testing.T) {
	for _, value := range []string{
		"postgres://user:pass@word@db.invalid/db",
		"postgres://user:pass@db%40other.invalid/db",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BANHMI_DATABASE_URL", value)
			got, err := configuredDatabaseURL()
			const want = "BANHMI_DATABASE_URL: invalid connection URI; percent-encode @ in the password"
			if got != "" || err == nil || err.Error() != want {
				t.Fatalf("configuredDatabaseURL() = %q, %v, want fixed validation error", got, err)
			}
		})
	}
}
