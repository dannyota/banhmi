package service

import "testing"

func TestRelationLabel(t *testing.T) {
	cases := map[string]string{
		"vbpl_type_8":        "related", // unmapped raw VBPL code → neutral label
		"vbpl_type_1":        "related",
		"legal_basis":        "legal_basis",
		"amends_supplements": "amends_supplements",
		"replaces":           "replaces",
	}
	for in, want := range cases {
		if got := relationLabel(in); got != want {
			t.Errorf("relationLabel(%q) = %q, want %q", in, want, got)
		}
	}
}
