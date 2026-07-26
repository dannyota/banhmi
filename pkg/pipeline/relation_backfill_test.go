package pipeline

import (
	"testing"
)

func TestParseRelationBackfillRef(t *testing.T) {
	ref, ok := parseRelationBackfillRef([]byte(`{
		"source":" vbpl ",
		"target_id":" 140561 ",
		"target_number":" 15/2020/NĐ-CP ",
		"target_title":" Nghị định 15 "
	}`))
	if !ok {
		t.Fatal("parseRelationBackfillRef ok = false, want true")
	}
	if ref.Source != "vbpl" || ref.TargetID != "140561" ||
		ref.TargetNumber != "15/2020/NĐ-CP" || ref.TargetTitle != "Nghị định 15" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestIsVBPLTranslationTarget(t *testing.T) {
	tests := []struct {
		name     string
		targetID string
		want     bool
	}{
		{"english rendition", "vbpqta_11014", true},
		{"english rendition with spaces", "  vbpqta_7382 ", true},
		{"english rendition uppercase", "VBPQTA_3038", true},
		{"normal numeric target", "18654", false},
		{"empty", "", false},
		{"unrelated prefix", "vbpq_1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isVBPLTranslationTarget(tt.targetID); got != tt.want {
				t.Fatalf("isVBPLTranslationTarget(%q) = %v, want %v", tt.targetID, got, tt.want)
			}
		})
	}
}

// TestBackfillSourceAllowed pins the per-country gate. Backfill enqueues a fetch
// keyed by (source, external_id), so a source qualifies only where its relation
// payload carries the target's source id — vbpl today, nothing else in any
// jurisdiction. An empty list must admit nothing rather than everything.
func TestBackfillSourceAllowed(t *testing.T) {
	vn := []string{"vbpl"}
	for _, tc := range []struct {
		name    string
		sources []string
		source  string
		want    bool
	}{
		{"vn allows vbpl", vn, "vbpl", true},
		{"vn rejects congbao", vn, "congbao", false},
		{"vn rejects an ID source", vn, "bpk", false},
		{"empty list admits nothing", nil, "vbpl", false},
		{"empty list admits nothing even for bi", []string{}, "bi", false},
		{"whitespace is trimmed", vn, " vbpl ", true},
		{"empty source never matches", vn, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := backfillSourceAllowed(tc.sources, tc.source); got != tc.want {
				t.Errorf("backfillSourceAllowed(%v, %q) = %v, want %v", tc.sources, tc.source, got, tc.want)
			}
		})
	}
}
