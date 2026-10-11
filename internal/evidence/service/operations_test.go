package service

import (
	"context"
	"testing"

	"danny.vn/banhmi/internal/evidence/api"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

type evidenceRetriever struct {
	evidence retrieve.Evidence
	calls    int
	opts     retrieve.SearchOpts
}

func (r *evidenceRetriever) SearchEvidence(_ context.Context, _ string, opts retrieve.SearchOpts) (retrieve.Evidence, error) {
	r.calls++
	r.opts = opts
	return r.evidence, nil
}

func TestUnknownSearchDetailUsesStandardFallback(t *testing.T) {
	retriever := &evidenceRetriever{}
	ops, err := NewOperations(retriever, nil, api.GuideOutput{}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Search(context.Background(), api.SearchInput{Query: "law", Detail: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if retriever.calls != 1 || retriever.opts.RelatedK != 8 {
		t.Fatalf("calls = %d related_k = %d, want one call with standard related_k=8", retriever.calls, retriever.opts.RelatedK)
	}
}

func TestSearchPreservesCitationProvenanceAndGaps(t *testing.T) {
	retriever := &evidenceRetriever{evidence: retrieve.Evidence{
		Hits: []retrieve.Hit{{
			ChunkID:    10,
			DocumentID: 20,
			DocNumber:  "11/2026/TT-NHNN",
			Title:      "Quy định",
			Source:     "vbpl",
			SourceURL:  "https://example.test/document/20",
			Citation:   "Điều 1",
			Content:    "Nội dung chính xác.",
			Validity: retrieve.ValidityEvidence{
				StatusClass: "unknown",
			},
			Text: retrieve.TextEvidence{
				HasNonBindingText: true,
				NeedsReview:       true,
			},
		}},
		Gaps: []retrieve.Gap{{
			Kind:         retrieve.GapKnownBindingTextGap,
			Message:      "binding text is missing",
			BlocksAnswer: true,
			DocumentID:   20,
			DocNumber:    "11/2026/TT-NHNN",
		}},
		Scope: retrieve.ScopeEvidence{
			Checked:      true,
			InDomain:     true,
			MatchedTerms: []string{"ngân hàng"},
		},
	}}
	ops, err := NewOperations(retriever, nil, api.GuideOutput{}, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ops.Search(context.Background(), api.SearchInput{Query: "ngân hàng"})
	if err != nil {
		t.Fatal(err)
	}
	if retriever.calls != 1 {
		t.Fatalf("retriever calls = %d, want 1", retriever.calls)
	}
	if len(out.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(out.Hits))
	}
	hit := out.Hits[0]
	wantCite := "Điều 1, 11/2026/TT-NHNN — Validity unknown — verify against the official source — https://example.test/document/20"
	if hit.Cite != wantCite {
		t.Fatalf("cite = %q, want %q", hit.Cite, wantCite)
	}
	if hit.Text.HasBindingText || !hit.Text.HasNonBindingText || !hit.Text.NeedsReview {
		t.Fatalf("text provenance = %+v", hit.Text)
	}
	if hit.Text.Quality != "non-binding text, needs review" {
		t.Fatalf("quality = %q", hit.Text.Quality)
	}
	if len(out.Gaps) != 1 || out.Gaps[0].Kind != string(retrieve.GapKnownBindingTextGap) ||
		out.Gaps[0].Message != "binding text is missing" || !out.Gaps[0].BlocksAnswer {
		t.Fatalf("gaps = %+v", out.Gaps)
	}
	if !out.Scope.Checked || !out.Scope.InDomain || len(out.Scope.MatchedTerms) != 1 {
		t.Fatalf("scope = %+v", out.Scope)
	}
}
