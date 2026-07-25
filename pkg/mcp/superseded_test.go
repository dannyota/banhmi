package mcp

import (
	"strings"
	"testing"
)

// The contradiction warning fires only when a superseding relation was recorded,
// names every superseding document, and never claims banhmi corrected the badge.
func TestSupersededWarning(t *testing.T) {
	if got := supersededWarning(nil); got != "" {
		t.Errorf("no superseding relation must yield no warning, got %q", got)
	}
	got := supersededWarning([]string{"52/2024/NĐ-CP", "70/2024/NĐ-CP"})
	for _, want := range []string{"52/2024/NĐ-CP", "70/2024/NĐ-CP", "does not override"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning missing %q: %s", want, got)
		}
	}
}

// A second data-quality signal must never silently replace the first — both the
// inconsistent-dates warning and the supersession contradiction have to survive.
func TestJoinWarnings(t *testing.T) {
	dates := validityWarning("2025-12-31", "2025-03-01") // effective before issued
	if dates == "" {
		t.Fatal("expected an inconsistent-date warning to build the fixture")
	}
	super := supersededWarning([]string{"52/2024/NĐ-CP"})

	both := joinWarnings(dates, super)
	if !strings.Contains(both, "precedes the issue date") || !strings.Contains(both, "52/2024/NĐ-CP") {
		t.Errorf("both warnings must survive, got: %s", both)
	}
	if got := joinWarnings("", super); got != super {
		t.Errorf("a single warning must pass through unchanged, got %q", got)
	}
	if got := joinWarnings("", ""); got != "" {
		t.Errorf("no warnings must yield empty, got %q", got)
	}
}
