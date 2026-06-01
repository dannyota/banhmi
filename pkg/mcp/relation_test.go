package mcp

import (
	"testing"

	"danny.vn/banhmi/pkg/rag/retrieve"
)

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

func TestToSearchRelationsLiteVsFull(t *testing.T) {
	rels := []retrieve.Relation{{
		RelationType:   "vbpl_type_9",
		DocNumber:      "91/2025/QH15",
		TargetValidity: retrieve.ValidityEvidence{StatusClass: "in_force"},
		TargetText:     retrieve.TextEvidence{HasBindingText: true},
		Evidence:       retrieve.RelationEvidence{EvidenceKind: "structured_relation"},
	}}

	// Lite (search pack): neutral label, compact validity kept, heavy fields dropped.
	lite := toSearchRelations(rels, false)
	if len(lite) != 1 || lite[0].RelationType != "related" {
		t.Fatalf("lite relation = %+v, want type 'related'", lite)
	}
	if lite[0].TargetText != nil || lite[0].Evidence != nil {
		t.Errorf("lite relation should drop target_text/evidence, got %+v / %+v",
			lite[0].TargetText, lite[0].Evidence)
	}
	if lite[0].TargetValidity == nil || lite[0].TargetValidity.StatusClass != "in_force" {
		t.Errorf("lite relation should keep compact target validity, got %+v", lite[0].TargetValidity)
	}

	// Full (document tool): includes the verbose target text + evidence.
	full := toSearchRelations(rels, true)
	if full[0].TargetText == nil || !full[0].TargetText.HasBindingText {
		t.Errorf("full relation should include target_text_provenance")
	}
	if full[0].Evidence == nil || full[0].Evidence.EvidenceKind != "structured_relation" {
		t.Errorf("full relation should include evidence, got %+v", full[0].Evidence)
	}
}
