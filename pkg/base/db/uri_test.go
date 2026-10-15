package db

import (
	"strings"
	"testing"
)

func TestValidateConnectionURI(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value string
		bad   bool
	}{
		{"plain", "postgres://user:pass@db.invalid/db", false},
		{"encoded at", "postgres://user:pass%40word@db.invalid/db", false},
		{"encoded percent", "postgres://user:pass%2540word@db.invalid/db", false},
		{"IPv6", "postgresql://user:pass@[::1]:5432/db", false},
		{"multiple hosts", "postgres://user:pass@db.invalid:5432,other.invalid:5433/db", false},
		{"multiple IPv6 hosts", "postgres://user:pass@[::1]:5432,[::2]:5433/db", false},
		{"keyword value", "host=db.invalid user=user password='pass@word'", false},
		{"keyword URI value", "password='postgres://pass@@word' host=db.invalid", false},
		{"path at", "postgres://user@db.invalid/db@name", false},
		{"query at", "postgres://user@db.invalid?application_name=a@b", false},
		{"empty", "", false},
		{"raw password at", "postgres://user:pass@word@db.invalid/db", true},
		{"no path", "postgresql://user:pass@word@db.invalid", true},
		{"query delimiter", "postgres://user:pass@word@db.invalid?sslmode=disable", true},
		{"raw host at", "postgres://user:pass@db@other.invalid/db", true},
		{"encoded host at", "postgres://user:pass@db%40other.invalid/db", true},
		{"encoded host without user", "postgres://db%40other.invalid/db", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConnectionURI("DATABASE_URL", tt.value)
			if !tt.bad {
				if err != nil {
					t.Fatalf("valid connection string rejected: %v", err)
				}
				return
			}
			const want = "DATABASE_URL: invalid connection URI; percent-encode @ in the password"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want fixed validation error", err)
			}
			for _, fragment := range []string{tt.value, "user", "pass@word", "db.invalid", "other.invalid"} {
				if strings.Contains(err.Error(), fragment) {
					t.Fatal("validation error contains connection content")
				}
			}
		})
	}
}
