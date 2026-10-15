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
		{"encoded delimiters", "postgres://user%3F%23:pass%3F%23%40word@db.invalid/db", false},
		{"IPv6", "postgresql://user:pass@[::1]:5432/db", false},
		{"multiple hosts", "postgres://user:pass@db.invalid:5432,other.invalid:5433/db", false},
		{"multiple IPv6 hosts", "postgres://user:pass@[::1]:5432,[::2]:5433/db", false},
		{"keyword value", "host=db.invalid user=user password='pass@word'", false},
		{"keyword URI value", "password='postgres://pass@@word' host=db.invalid", false},
		{"path at", "postgres://user@db.invalid/db@name", false},
		{"query at after path", "postgres://user@db.invalid/db?application_name=a@b", false},
		{"query without userinfo", "postgres://db.invalid?application_name=example", false},
		{"encoded query at without path", "postgres://db.invalid?password=pass%40word", false},
		{"fragment at after path", "postgresql://user@db.invalid/db#part@word", false},
		{"empty", "", false},
		{"raw password at", "postgres://user:pass@word@db.invalid/db", true},
		{"no path", "postgresql://user:pass@word@db.invalid", true},
		{"query delimiter", "postgres://user:pass@word@db.invalid?sslmode=disable", true},
		{"raw host at", "postgres://user:pass@db@other.invalid/db", true},
		{"encoded host at", "postgres://user:pass@db%40other.invalid/db", true},
		{"encoded host without user", "postgres://db%40other.invalid/db", true},
		{"query consumed as user", "postgres://db.invalid?password=pass@word", true},
		{"long scheme query consumed as user", "postgresql://db.invalid?password=pass@word", true},
		{"query in password with extra at", "postgres://user:pass?part@word@db.invalid/db", true},
		{"long scheme query in password with extra at", "postgresql://user:pass?part@word@db.invalid/db", true},
		{"query in password with one at", "postgres://user:pass?part@db.invalid/db", true},
		{"fragment in user", "postgres://user#part:pass@db.invalid/db", true},
		{"fragment in password", "postgresql://user:pass#part@db.invalid/db", true},
		{"query at without path", "postgres://user@db.invalid?application_name=a@b", true},
		// Fail closed even when query overrides would hide the ambiguous authority.
		// No deployed configuration is known to require this shape.
		{"query overrides ambiguous authority", "postgres://user:ignored@fragment@placeholder/db?host=db.invalid&password=pass", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConnectionURI("DATABASE_URL", tt.value)
			if !tt.bad {
				if err != nil {
					t.Fatalf("valid connection string rejected: %v", err)
				}
				return
			}
			const want = "DATABASE_URL: invalid connection URI; percent-encode @, ?, and # in the user and password"
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want fixed validation error", err)
			}
			for _, fragment := range []string{tt.value, "user:", "pass@word", "pass?part", "pass#part", "db.invalid", "other.invalid", "ignored", "placeholder"} {
				if strings.Contains(err.Error(), fragment) {
					t.Fatal("validation error contains connection content")
				}
			}
		})
	}
}
