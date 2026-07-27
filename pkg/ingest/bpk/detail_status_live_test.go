package bpk

import "testing"

// TestParseDetailRelationsLiveMarkup pins relation extraction against a real
// capture. The pre-existing fixture was hand-authored, and the live markup had
// drifted away from it: the type badge renders as "col-12 fw-semibold
// bg-light-primary p-4" with no text-primary, so the badge regex matched
// nothing and BPK produced zero relations across all 802 detail pages — even
// though the target link, and its /Details/<id>, were sitting right there.
func TestParseDetailRelationsLiveMarkup(t *testing.T) {
	body := readTestdata(t, "detail_status_live_20260727.html")

	rels := parseDetailRelations(body)
	if len(rels) != 1 {
		t.Fatalf("parseDetailRelations returned %d relations, want 1: %+v", len(rels), rels)
	}
	got := rels[0]

	// The operator must match config.relation_type.code exactly ("Dicabut
	// dengan", not "Dicabut dengan :"), or the edge never resolves to a label
	// and silently stays weak.
	if got.Type != "Dicabut dengan" {
		t.Errorf("Type = %q, want %q", got.Type, "Dicabut dengan")
	}
	// The target id is what makes relation backfill possible for Indonesia:
	// it is the only ID source that supplies one.
	if got.TargetID != "168895" {
		t.Errorf("TargetID = %q, want %q", got.TargetID, "168895")
	}
	if got.TargetNumber != "Perka PPATK No. 03 Tahun 2017" {
		t.Errorf("TargetNumber = %q", got.TargetNumber)
	}
	// Current markup puts the subject AFTER the "tentang" span, not inside it.
	if got.TargetTitle == "" {
		t.Error("TargetTitle is empty; the subject follows the tentang span in live markup")
	}
}
