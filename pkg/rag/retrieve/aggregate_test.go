package retrieve

import (
	"testing"
)

// aggMY calls aggregateBySection with the MY article prefix.
func aggMY(hits []Hit, topN int) []Hit {
	return aggregateBySection(hits, "section ", topN)
}

// aggVN calls aggregateBySection with the VN article prefix.
func aggVN(hits []Hit, topN int) []Hit {
	return aggregateBySection(hits, "điều ", topN)
}

// aggID calls aggregateBySection with the ID article prefix.
func aggID(hits []Hit, topN int) []Hit {
	return aggregateBySection(hits, "pasal ", topN)
}

func TestAggregateBySection(t *testing.T) {
	hit := func(docID int64, citation string, score float64, chunkID int64) Hit {
		return Hit{
			ChunkID:    chunkID,
			DocumentID: docID,
			Citation:   citation,
			Score:      score,
		}
	}

	t.Run("basic aggregation promotes a multi-fragment section", func(t *testing.T) {
		// MY Act 854 Section 22 problem: Section 22 has 3 subsection fragments
		// each scoring ~0.010, while Section 10 has 1 fragment at 0.012.
		// Sum-of-top-3: Section 22 = 0.011+0.010+0.009 = 0.030 > 0.012.
		hits := []Hit{
			hit(1, "Section 10", 0.012, 100),
			hit(1, "Section 22, (1)", 0.011, 201),
			hit(1, "Section 22, (2)", 0.010, 202),
			hit(1, "Section 22, (3)", 0.009, 203),
		}
		got := aggMY(hits, 3)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].Citation != "Section 22, (1)" {
			t.Errorf("got[0].Citation = %q, want Section 22, (1)", got[0].Citation)
		}
		if got[0].ChunkID != 201 {
			t.Errorf("got[0].ChunkID = %d, want 201 (best member)", got[0].ChunkID)
		}
		if got[1].Citation != "Section 10" {
			t.Errorf("got[1].Citation = %q, want Section 10", got[1].Citation)
		}
	})

	t.Run("top-N cap prevents volume-based crowding", func(t *testing.T) {
		// 6 mediocre fragments (each 0.005): sum top-3 = 0.015 < 0.020.
		hits := []Hit{
			hit(1, "Section 1", 0.020, 10),
			hit(1, "Section 2, (1)", 0.005, 21),
			hit(1, "Section 2, (2)", 0.005, 22),
			hit(1, "Section 2, (3)", 0.005, 23),
			hit(1, "Section 2, (4)", 0.005, 24),
			hit(1, "Section 2, (5)", 0.005, 25),
			hit(1, "Section 2, (6)", 0.005, 26),
		}
		got := aggMY(hits, 3)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].Citation != "Section 1" {
			t.Errorf("got[0] = %q, want Section 1 (one excellent chunk beats 6 mediocre)", got[0].Citation)
		}
	})

	t.Run("cross-document groups stay separate", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Section 5, (1)", 0.010, 101),
			hit(2, "Section 5, (1)", 0.009, 201),
		}
		got := aggMY(hits, 3)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (cross-document)", len(got))
		}
		if got[0].DocumentID != 1 || got[1].DocumentID != 2 {
			t.Errorf("cross-doc order wrong: got doc %d, %d", got[0].DocumentID, got[1].DocumentID)
		}
	})

	t.Run("single-member groups degenerate to score sort", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Section 3", 0.015, 30),
			hit(1, "Section 1", 0.020, 10),
			hit(1, "Section 2", 0.010, 20),
		}
		got := aggMY(hits, 3)
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		if got[0].ChunkID != 10 || got[1].ChunkID != 30 || got[2].ChunkID != 20 {
			t.Errorf("score order wrong: %d, %d, %d", got[0].ChunkID, got[1].ChunkID, got[2].ChunkID)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		got := aggMY(nil, 3)
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})

	t.Run("deterministic tie-break by chunk ID", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Section A", 0.010, 200),
			hit(1, "Section B", 0.010, 100),
		}
		got := aggMY(hits, 3)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		if got[0].ChunkID != 100 {
			t.Errorf("tie-break: got chunk %d first, want 100 (lower ID)", got[0].ChunkID)
		}
	})

	t.Run("VN-style Dieu aggregation groups Khoan", func(t *testing.T) {
		// Multiple Khoan under the same Dieu aggregate at article level.
		hits := []Hit{
			hit(1, "Điều 5, Khoản 1", 0.014, 50),
			hit(1, "Điều 10, Khoản 2", 0.008, 101),
			hit(1, "Điều 10, Khoản 3", 0.007, 102),
			hit(1, "Điều 10, Khoản 4", 0.006, 103),
		}
		got := aggVN(hits, 3)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		// Dieu 10 aggregate = 0.021 > Dieu 5 = 0.014.
		if got[0].Citation != "Điều 10, Khoản 2" {
			t.Errorf("got[0] = %q, want Dieu 10, Khoan 2 (best member)", got[0].Citation)
		}
	})

	t.Run("ID-style Pasal/Ayat aggregation", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Pasal 5", 0.013, 50),
			hit(1, "Pasal 12, Ayat (1)", 0.009, 121),
			hit(1, "Pasal 12, Ayat (2)", 0.008, 122),
			hit(1, "Pasal 12, Ayat (3)", 0.007, 123),
		}
		got := aggID(hits, 3)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		// Pasal 12 aggregate = 0.024 > Pasal 5 = 0.013.
		if got[0].Citation != "Pasal 12, Ayat (1)" {
			t.Errorf("got[0] = %q, want Pasal 12, Ayat (1)", got[0].Citation)
		}
	})

	t.Run("preserves ParentCitation from rollup", func(t *testing.T) {
		hits := []Hit{
			{ChunkID: 101, DocumentID: 1, Citation: "Section 22, (1)", ParentCitation: "Section 22, (1)", Score: 0.011},
			{ChunkID: 102, DocumentID: 1, Citation: "Section 22, (2)", ParentCitation: "Section 22, (2)", Score: 0.010},
			{ChunkID: 100, DocumentID: 1, Citation: "Section 10", ParentCitation: "Section 10", Score: 0.012},
		}
		got := aggMY(hits, 3)
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2", len(got))
		}
		// Best member's ParentCitation preserved.
		if got[0].ParentCitation != "Section 22, (1)" {
			t.Errorf("got[0].ParentCitation = %q, want preserved from rollup", got[0].ParentCitation)
		}
	})

	t.Run("article-level citation extraction", func(t *testing.T) {
		tests := []struct {
			citation, prefix, want string
		}{
			{"Section 22, (1)", "section ", "Section 22"},
			{"Section 10", "section ", "Section 10"},
			{"Điều 7, Khoản 2, Điểm a", "điều ", "Điều 7"},
			{"Pasal 12, Ayat (3), Huruf b", "pasal ", "Pasal 12"},
			{"Chapter II", "section ", "Chapter II"}, // no match -> full citation
			{"", "section ", ""},
		}
		for _, tt := range tests {
			got := articleLevelCitation(tt.citation, tt.prefix)
			if got != tt.want {
				t.Errorf("articleLevelCitation(%q, %q) = %q, want %q",
					tt.citation, tt.prefix, got, tt.want)
			}
		}
	})
}

// TestAggregateOffPathIdentity verifies that when section aggregation is
// disabled (the default), the search path produces byte-identical output.
// When all groups are singletons, aggregation preserves score order.
func TestAggregateOffPathIdentity(t *testing.T) {
	hits := []Hit{
		{ChunkID: 1, DocumentID: 1, Citation: "Section 7", ParentCitation: "Section 7", Score: 0.020},
		{ChunkID: 2, DocumentID: 1, Citation: "Section 8", ParentCitation: "Section 8", Score: 0.018},
		{ChunkID: 3, DocumentID: 2, Citation: "Section 10", ParentCitation: "Section 10", Score: 0.016},
		{ChunkID: 4, DocumentID: 2, Citation: "Section 22", ParentCitation: "Section 22", Score: 0.014},
		{ChunkID: 5, DocumentID: 3, Citation: "Section 5", ParentCitation: "Section 5", Score: 0.012},
		{ChunkID: 6, DocumentID: 1, Citation: "Section 19", ParentCitation: "Section 19", Score: 0.010},
		{ChunkID: 7, DocumentID: 3, Citation: "Section 12", ParentCitation: "Section 12", Score: 0.008},
		{ChunkID: 8, DocumentID: 2, Citation: "Section 3", ParentCitation: "Section 3", Score: 0.006},
	}

	original := make([]Hit, len(hits))
	copy(original, hits)

	got := aggMY(hits, 3)
	if len(got) != len(original) {
		t.Fatalf("singleton aggregation changed length: %d -> %d", len(original), len(got))
	}
	for i := range got {
		if got[i].ChunkID != original[i].ChunkID {
			t.Errorf("position %d: ChunkID %d, want %d", i, got[i].ChunkID, original[i].ChunkID)
		}
		if got[i].Score != original[i].Score {
			t.Errorf("position %d: Score %f, want %f", i, got[i].Score, original[i].Score)
		}
	}
}

// TestAggregateBehaviorsMatchRollupWhenNoSiblings confirms that on hits with no
// sibling fragments within an article, aggregation preserves the rollup order.
func TestAggregateBehaviorsMatchRollupWhenNoSiblings(t *testing.T) {
	ap, sap := "điều ", "khoản "
	hits := []Hit{
		{ChunkID: 10, DocumentID: 1, Citation: "Điều 18, Khoản 2", Score: 0.9},
		{ChunkID: 20, DocumentID: 1, Citation: "Điều 17, Khoản 2", Score: 0.8},
		{ChunkID: 30, DocumentID: 2, Citation: "Điều 3", Score: 0.7},
		{ChunkID: 40, DocumentID: 2, Citation: "Điều 5, Khoản 1", Score: 0.6},
	}

	h1 := make([]Hit, len(hits))
	copy(h1, hits)
	h2 := make([]Hit, len(hits))
	copy(h2, hits)

	rolledUp := rollupByParent(h1, rollupKhoan, ap, sap)
	rolledUp2 := rollupByParent(h2, rollupKhoan, ap, sap)
	aggregated := aggVN(rolledUp2, 3)

	if len(rolledUp) != len(aggregated) {
		t.Fatalf("len mismatch: rollup=%d, aggregate=%d", len(rolledUp), len(aggregated))
	}
	for i := range rolledUp {
		if rolledUp[i].ChunkID != aggregated[i].ChunkID {
			t.Errorf("pos %d: rollup chunk=%d, aggregate chunk=%d", i, rolledUp[i].ChunkID, aggregated[i].ChunkID)
		}
	}
}
