package main

import (
	"strings"
	"testing"
)

func TestDevDBIdentifier(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"banhmi_atlasdev", `"banhmi_atlasdev"`},
		{"laksa_atlasdev", `"laksa_atlasdev"`},
		{"Corpus-01_atlasdev", `"Corpus-01_atlasdev"`},
		{strings.Repeat("a", 54) + "_atlasdev", `"` + strings.Repeat("a", 54) + `_atlasdev"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := devDBIdentifier(tt.name)
			if err != nil {
				t.Fatalf("devDBIdentifier(%q): %v", tt.name, err)
			}
			if got != tt.want {
				t.Fatalf("devDBIdentifier(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestDevDBIdentifierRejectsUnsafeNames(t *testing.T) {
	for _, name := range []string{
		"",
		"banhmi",
		"_atlasdev",
		strings.Repeat("a", 55) + "_atlasdev",
		`banhmi"; DROP DATABASE other; --_atlasdev`,
		"banhmi;DROP_DATABASE_other_atlasdev",
		"banhmi/schema_atlasdev",
		"banhmi?sslmode=disable_atlasdev",
		"banhmi#fragment_atlasdev",
		"banhmi%2fother_atlasdev",
		"banhmi.name_atlasdev",
		"banhmi name_atlasdev",
		"banhmi\nname_atlasdev",
		"banhmi\x00name_atlasdev",
		"ngân_atlasdev",
		"\xff_atlasdev",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := devDBIdentifier(name)
			if err == nil || err.Error() != "invalid temporary database name" {
				t.Fatalf("devDBIdentifier returned %q, %v; want fixed validation error", got, err)
			}
			if got != "" {
				t.Fatalf("invalid name produced SQL identifier %q", got)
			}
		})
	}
}

func TestCreateDevDBRejectsInvalidNameBeforeConfig(t *testing.T) {
	// A nil config must remain untouched because validation precedes connection setup.
	url, cleanup, err := createDevDB(nil, "unsafe;name_atlasdev")
	if err == nil || err.Error() != "invalid temporary database name" {
		t.Fatalf("createDevDB error = %v, want fixed validation error", err)
	}
	if url != "" || cleanup != nil {
		t.Fatal("invalid name returned a database URL or cleanup function")
	}
}
