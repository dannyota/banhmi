package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestConfiguredDatabaseURLRejectsAmbiguousURIBeforeDiscovery(t *testing.T) {
	for _, value := range []string{
		"postgres://user:pass@word@db.invalid/db",
		"postgres://user:pass@db%40other.invalid/db",
		"postgres://db.invalid?password=pass@word",
		"postgresql://db.invalid?password=pass@word",
		"postgres://user:pass?part@word@db.invalid/db",
		"postgresql://user:pass?part@word@db.invalid/db",
	} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BANHMI_DATABASE_URL", value)
			got, err := configuredDatabaseURL()
			const want = "BANHMI_DATABASE_URL: invalid connection URI; percent-encode @, ?, and # in the user and password"
			if got != "" || err == nil || err.Error() != want {
				t.Fatalf("configuredDatabaseURL() = %q, %v, want fixed validation error", got, err)
			}
			var output bytes.Buffer
			log.New(&output, "", 0).Print(err)
			for _, fragment := range []string{value, "db.invalid", "pass@word", "pass?part", "user:"} {
				if strings.Contains(output.String(), fragment) {
					t.Fatal("logged error contains connection content")
				}
			}
		})
	}
}
