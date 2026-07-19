package retrieve

import (
	"context"
	"testing"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/scope"
)

func TestScopeEvidenceUsesConfiguredTerms(t *testing.T) {
	r := New(nil, nil, config.RetrieveConfig{}, nil, WithGateConfig(GateConfig{
		ScopeTerms: []scope.Term{
			{Text: "alpha scope", Class: scope.ClassStrong},
			{Text: "beta system", Class: scope.ClassWeak},
			{Text: "bank signal", Class: scope.ClassSignal},
		},
	})).(*hybridRetriever)

	got, refs, err := r.scopeEvidence(context.Background(), "question about alpha scope")
	if err != nil {
		t.Fatalf("scopeEvidence strong: %v", err)
	}
	if !got.Checked || !got.InDomain {
		t.Fatalf("strong scope = %+v, want checked in-domain", got)
	}
	if len(got.MatchedTerms) != 1 || got.MatchedTerms[0] != "alpha scope" {
		t.Fatalf("matched terms = %+v", got.MatchedTerms)
	}
	if len(refs) != 0 {
		t.Fatalf("refs = %+v, want none", refs)
	}

	got, _, err = r.scopeEvidence(context.Background(), "unrelated cooking question")
	if err != nil {
		t.Fatalf("scopeEvidence unrelated: %v", err)
	}
	if !got.Checked || got.InDomain {
		t.Fatalf("unrelated scope = %+v, want checked out-of-domain", got)
	}

	got, _, err = r.scopeEvidence(context.Background(), "bank signal deploys beta system")
	if err != nil {
		t.Fatalf("scopeEvidence weak+signal: %v", err)
	}
	if !got.InDomain {
		t.Fatalf("weak+signal scope = %+v, want in-domain", got)
	}
}

func TestExtractDocumentRefs(t *testing.T) {
	got := extractDocumentRefs("Compare 01/2026/TT-ABC and 02/2026/QD-ABC")
	want := map[string]bool{"01/2026/tt-abc": true, "02/2026/qd-abc": true}
	if len(got) != len(want) {
		t.Fatalf("refs = %+v, want %v", got, want)
	}
	for _, ref := range got {
		if !want[ref] {
			t.Fatalf("unexpected ref %q in %+v", ref, got)
		}
	}
}

func TestExtractDocumentRefsIndonesian(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"What does UU 27/2022 regulate?", []string{"uu 27/2022"}},
		{"PP 71/2019 data center rules", []string{"pp 71/2019"}},
		{"Perpres 47/2023 isinya tentang apa?", []string{"perpres 47/2023"}},
		{"PMK 68/PMK.03/2022 tentang pajak kripto", []string{"pmk 68/pmk.03/2022"}},
		{"Perppu 2/2022 tentang Cipta Kerja", []string{"perppu 2/2022"}},
		{"POJK 11/POJK.03/2022 tentang TI", []string{"pojk 11/pojk.03/2022"}},
		{"Is PBI 10/2025 still valid?", []string{"pbi 10/2025"}},
		{"PADG 15/2024 bilateral", []string{"padg 15/2024"}},
		{"SEOJK 29/2022 requirements", []string{"seojk 29/2022"}},
	}
	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := extractDocumentRefs(tt.query)
			if len(got) != len(tt.want) {
				t.Fatalf("refs = %v, want %v", got, tt.want)
			}
			for i, ref := range got {
				if ref != tt.want[i] {
					t.Fatalf("ref[%d] = %q, want %q", i, ref, tt.want[i])
				}
			}
		})
	}
}

func TestExpandIndonesianRef(t *testing.T) {
	tests := []struct {
		ref  string
		want []string
	}{
		// UU: Nomor X Tahun YYYY
		{"uu 27/2022", []string{"nomor 27 tahun 2022"}},
		// PP: Nomor X Tahun YYYY
		{"pp 71/2019", []string{"nomor 71 tahun 2019"}},
		// Perpres: Nomor X Tahun YYYY
		{"perpres 47/2023", []string{"nomor 47 tahun 2023"}},
		// Perppu: Nomor X Tahun YYYY
		{"perppu 2/2022", []string{"nomor 2 tahun 2022"}},
		// PMK slash-form: body as-is + Nomor X Tahun YYYY
		{"pmk 68/pmk.03/2022", []string{"68/pmk.03/2022", "nomor 68 tahun 2022"}},
		// PMK simple: Nomor X Tahun YYYY
		{"pmk 133/2022", []string{"nomor 133 tahun 2022"}},
		// POJK slash-form: body as-is + Nomor X Tahun YYYY
		{"pojk 11/pojk.03/2022", []string{"11/pojk.03/2022", "nomor 11 tahun 2022"}},
		// POJK new-style (simple number/year): only Nomor
		{"pojk 21/2023", []string{"nomor 21 tahun 2023"}},
		// PBI: No.X Tahun YYYY
		{"pbi 10/2025", []string{"no.10 tahun 2025"}},
		// PADG: No.X Tahun YYYY
		{"padg 15/2024", []string{"no.15 tahun 2024"}},
		// SEOJK: Nomor X Tahun YYYY
		{"seojk 29/2022", []string{"nomor 29 tahun 2022"}},
		// SEOJK slash-form
		{"seojk 14/seojk.07/2024", []string{"14/seojk.07/2024", "nomor 14 tahun 2024"}},
		// Non-Indonesian ref: no expansion
		{"50/2024/tt-nhnn", nil},
		// Act: no expansion
		{"act 854", nil},
	}
	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			got := expandIndonesianRef(tt.ref)
			if len(got) != len(tt.want) {
				t.Fatalf("expandIndonesianRef(%q) = %v, want %v", tt.ref, got, tt.want)
			}
			for i, v := range got {
				if v != tt.want[i] {
					t.Fatalf("expandIndonesianRef(%q)[%d] = %q, want %q", tt.ref, i, v, tt.want[i])
				}
			}
		})
	}
}

func TestEvidenceBlockingGaps(t *testing.T) {
	ev := Evidence{}
	ev.addGap(Gap{Kind: GapUnresolvedRelation})
	if hasBlockingGap(ev.Gaps) {
		t.Fatal("warning relation gap should not block")
	}
	ev.addGap(Gap{Kind: GapKnownBindingTextGap})
	if hasBlockingGap(ev.Gaps) {
		t.Fatal("binding text gap is context and should not block by itself")
	}
	ev.addGap(Gap{Kind: GapNoEvidence, BlocksAnswer: true})
	if !hasBlockingGap(ev.Gaps) {
		t.Fatal("no evidence gap should block")
	}
}

func TestEvidenceStateGapsSurfaceValidityAndReview(t *testing.T) {
	hits := []Hit{
		{
			DocumentID: 1,
			DocNumber:  "01/2026/TT-ABC",
			Title:      "Unknown validity",
			Text:       TextEvidence{HasBindingText: true},
		},
		{
			DocumentID: 2,
			DocNumber:  "02/2026/TT-ABC",
			Title:      "Partial validity",
			Validity:   ValidityEvidence{StatusClass: "partial"},
			Text:       TextEvidence{HasBindingText: true, NeedsReview: true},
		},
		{
			DocumentID: 3,
			DocNumber:  "03/2026/TT-ABC",
			Title:      "Section validity",
			Validity:   ValidityEvidence{SectionID: 33, StatusClass: "partial"},
			Text:       TextEvidence{HasBindingText: true},
		},
	}

	gaps := evidenceStateGaps(hits, nil)
	if !hasGap(gaps, GapValidityUnknown, 1) {
		t.Fatalf("gaps = %+v, want validity_unknown for doc 1", gaps)
	}
	if !hasGap(gaps, GapPartialValidityUncertain, 2) {
		t.Fatalf("gaps = %+v, want partial_validity_uncertain for doc 2", gaps)
	}
	if !hasGap(gaps, GapTextNeedsReview, 2) {
		t.Fatalf("gaps = %+v, want text_needs_review for doc 2", gaps)
	}
	if hasGap(gaps, GapPartialValidityUncertain, 3) {
		t.Fatalf("gaps = %+v, did not expect partial gap for section-level validity", gaps)
	}
}

func hasGap(gaps []Gap, kind GapKind, docID int64) bool {
	for _, gap := range gaps {
		if gap.Kind == kind && gap.DocumentID == docID {
			return true
		}
	}
	return false
}

func TestRelatedSeedsOnlyConfirmedIndexedBindingTargets(t *testing.T) {
	hits := []Hit{{
		ChunkID:    1,
		DocumentID: 10,
		DocNumber:  "01/2026/TT-ABC",
		Relations: []Relation{
			{
				RelationID:           7,
				Direction:            "outgoing",
				RelationType:         "legal_basis",
				Source:               "vbpl",
				DocumentID:           20,
				DocNumber:            "02/2026/ND-ABC",
				Resolved:             true,
				TargetIndexed:        true,
				TargetHasBindingText: true,
			},
			{
				RelationID:           8,
				Direction:            "outgoing",
				RelationType:         "replaces",
				DocumentID:           21,
				Resolved:             true,
				TargetIndexed:        true,
				TargetHasBindingText: false,
			},
			{
				RelationID:           7,
				Direction:            "outgoing",
				RelationType:         "legal_basis",
				DocumentID:           20,
				Resolved:             true,
				TargetIndexed:        true,
				TargetHasBindingText: true,
			},
		},
	}}

	got := relatedSeeds(hits)
	if len(got) != 1 {
		t.Fatalf("len(relatedSeeds) = %d, want 1: %+v", len(got), got)
	}
	seed := got[0]
	if seed.baseChunkID != 1 || seed.baseDocumentID != 10 || seed.relationID != 7 || seed.documentID != 20 {
		t.Fatalf("seed = %+v, want base chunk/doc and relation target preserved", seed)
	}
	if seed.sourceRank != 1 || seed.relationType != "legal_basis" || seed.source != "vbpl" {
		t.Fatalf("seed metadata = %+v, want relation provenance", seed)
	}
}
