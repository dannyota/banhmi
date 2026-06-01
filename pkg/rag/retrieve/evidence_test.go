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
