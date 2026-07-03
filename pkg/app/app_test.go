package app

import (
	"testing"

	"danny.vn/banhmi/pkg/base/jurisdiction"
)

// TestSourceBuildersCoverRegistry guards the composition root against registry
// drift: every jurisdiction in the registry must have a wired source set, and
// nothing may be wired for a code the registry does not know.
func TestSourceBuildersCoverRegistry(t *testing.T) {
	codes := map[string]bool{}
	for _, d := range jurisdiction.All() {
		codes[d.Code] = true
		if sourceBuilders[d.Code] == nil {
			t.Errorf("jurisdiction %q has no source builder wired in pkg/app", d.Code)
		}
	}
	for code := range sourceBuilders {
		if !codes[code] {
			t.Errorf("source builder wired for %q, which is not in the jurisdiction registry", code)
		}
	}
}

// TestResolveJurisdictionRejectsUnknown keeps startup fail-fast: a typo in
// BANHMI_JURISDICTION must abort composition, never silently serve the VN
// fallback with the wrong sources.
func TestResolveJurisdictionRejectsUnknown(t *testing.T) {
	if _, err := resolveJurisdiction(cfgWithJurisdiction("xx")); err == nil {
		t.Error("resolveJurisdiction(xx) = nil error, want unknown-jurisdiction error")
	}
	d, err := resolveJurisdiction(cfgWithJurisdiction("my"))
	if err != nil || d.Code != "my" {
		t.Errorf("resolveJurisdiction(my) = (%q, %v), want (my, nil)", d.Code, err)
	}
}
