package retrieve

import (
	"testing"
)

// aggMY calls aggregateBySection with the MY article prefix and topK=8.
func aggMY(hits []Hit, topN int) []Hit {
	return aggregateBySection(hits, "section ", topN, 8)
}

// aggVN calls aggregateBySection with the VN article prefix and topK=8.
func aggVN(hits []Hit, topN int) []Hit {
	return aggregateBySection(hits, "điều ", topN, 8)
}

// aggID calls aggregateBySection with the ID article prefix and topK=8.
func aggID(hits []Hit, topN int) []Hit {
	return aggregateBySection(hits, "pasal ", topN, 8)
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

	t.Run("promotion appends multi-fragment group after natural top-k", func(t *testing.T) {
		// 11 hits, topK=8: natural top-k is positions 0-7. Section 22 has 3
		// fragments outside the window (positions 8-10). Its aggregate score
		// (0.009+0.008+0.007=0.024) exceeds the weakest entry (0.003). Section
		// 22's best member should be APPENDED at position 8.
		hits := []Hit{
			hit(1, "Section 1", 0.020, 10),
			hit(1, "Section 2", 0.015, 20),
			hit(2, "Section 3", 0.012, 30),
			hit(2, "Section 4", 0.010, 40),
			hit(2, "Section 5", 0.006, 50),
			hit(2, "Section 6", 0.005, 60),
			hit(2, "Section 7", 0.004, 70),
			hit(2, "Section 8", 0.003, 80),
			hit(1, "Section 22, (1)", 0.009, 201),
			hit(1, "Section 22, (2)", 0.008, 202),
			hit(1, "Section 22, (3)", 0.007, 203),
		}
		got := aggMY(hits, 3)

		// Natural top-k (8 entries) must be byte-identical.
		for i := 0; i < 8; i++ {
			if got[i].ChunkID != hits[i].ChunkID {
				t.Errorf("natural pos %d: got chunk %d, want %d", i, got[i].ChunkID, hits[i].ChunkID)
			}
		}
		// Section 22's best member should be appended.
		if len(got) < 9 {
			t.Fatal("expected promoted entry appended at position 8")
		}
		if got[8].ChunkID != 201 {
			t.Errorf("promoted entry: got chunk %d, want 201", got[8].ChunkID)
		}
	})

	t.Run("natural top-k order never changes", func(t *testing.T) {
		// Section 22 fragments are in the natural top-k — no promotion needed.
		hits := []Hit{
			hit(1, "Section 10", 0.012, 100),
			hit(1, "Section 22, (1)", 0.011, 201),
			hit(1, "Section 22, (2)", 0.010, 202),
			hit(1, "Section 22, (3)", 0.009, 203),
			hit(1, "Section 5", 0.008, 50),
		}
		got := aggMY(hits, 3)
		// No promotion occurs — Section 22 is already in the natural top-k.
		if len(got) != len(hits) {
			t.Fatalf("len = %d, want %d", len(got), len(hits))
		}
		for i := range hits {
			if got[i].ChunkID != hits[i].ChunkID {
				t.Errorf("pos %d: got chunk %d, want %d (no change expected)", i, got[i].ChunkID, hits[i].ChunkID)
			}
		}
	})

	t.Run("single-member groups never promoted (minMembers gate)", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Section 1", 0.020, 10),
			hit(1, "Section 2", 0.015, 20),
			hit(2, "Section 3", 0.012, 30),
			hit(2, "Section 4", 0.010, 40),
			hit(1, "Section 9", 0.005, 90),
			hit(2, "Section 7", 0.004, 70),
		}
		got := aggMY(hits, 3)
		if len(got) != len(hits) {
			t.Fatalf("len = %d, want %d (no promotion for singletons)", len(got), len(hits))
		}
		for i := range got {
			if got[i].ChunkID != hits[i].ChunkID {
				t.Errorf("pos %d: got chunk %d, want %d", i, got[i].ChunkID, hits[i].ChunkID)
			}
		}
	})

	t.Run("cross-document groups stay separate", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Section 1", 0.020, 10),
			hit(2, "Section 2", 0.018, 20),
			hit(1, "Section 3", 0.016, 30),
			hit(2, "Section 4", 0.014, 40),
			hit(1, "Section 5, (1)", 0.010, 101),
			hit(2, "Section 5, (1)", 0.009, 201),
		}
		got := aggMY(hits, 3)
		if len(got) != len(hits) {
			t.Fatalf("len = %d, want %d", len(got), len(hits))
		}
	})

	t.Run("empty input", func(t *testing.T) {
		got := aggMY(nil, 3)
		if len(got) != 0 {
			t.Fatalf("len = %d, want 0", len(got))
		}
	})

	t.Run("VN-style Dieu: multi-Khoan article appended from outside window", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Điều 5, Khoản 1", 0.020, 50),
			hit(1, "Điều 3, Khoản 1", 0.018, 31),
			hit(2, "Điều 7", 0.016, 70),
			hit(2, "Điều 8, Khoản 1", 0.014, 81),
			hit(2, "Điều 12", 0.005, 120),
			hit(2, "Điều 15", 0.004, 150),
			hit(2, "Điều 16", 0.003, 160),
			hit(2, "Điều 17", 0.002, 170),
			hit(1, "Điều 10, Khoản 2", 0.008, 101),
			hit(1, "Điều 10, Khoản 3", 0.007, 102),
			hit(1, "Điều 10, Khoản 4", 0.006, 103),
		}
		got := aggVN(hits, 3)
		// Natural top-k (8) preserved.
		for i := 0; i < 8; i++ {
			if got[i].ChunkID != hits[i].ChunkID {
				t.Errorf("natural pos %d: got chunk %d, want %d", i, got[i].ChunkID, hits[i].ChunkID)
			}
		}
		// Dieu 10 (agg=0.021) appended.
		if len(got) < 9 || got[8].ChunkID != 101 {
			t.Error("Dieu 10 best member (chunk 101) not appended after natural top-k")
		}
	})

	t.Run("ID-style Pasal/Ayat appended from outside window", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Pasal 5", 0.020, 50),
			hit(1, "Pasal 3", 0.018, 30),
			hit(2, "Pasal 7", 0.016, 70),
			hit(2, "Pasal 8", 0.014, 80),
			hit(2, "Pasal 9", 0.005, 90),
			hit(2, "Pasal 11", 0.004, 110),
			hit(2, "Pasal 13", 0.003, 130),
			hit(2, "Pasal 14", 0.002, 140),
			hit(1, "Pasal 12, Ayat (1)", 0.009, 121),
			hit(1, "Pasal 12, Ayat (2)", 0.008, 122),
			hit(1, "Pasal 12, Ayat (3)", 0.007, 123),
		}
		got := aggID(hits, 3)
		if len(got) < 9 || got[8].ChunkID != 121 {
			t.Error("Pasal 12 best member not appended after natural top-k")
		}
	})

	t.Run("preserves ParentCitation from rollup", func(t *testing.T) {
		hits := []Hit{
			{ChunkID: 10, DocumentID: 1, Citation: "Section 1", ParentCitation: "Section 1", Score: 0.020},
			{ChunkID: 20, DocumentID: 1, Citation: "Section 2", ParentCitation: "Section 2", Score: 0.018},
			{ChunkID: 30, DocumentID: 2, Citation: "Section 3", ParentCitation: "Section 3", Score: 0.016},
			{ChunkID: 40, DocumentID: 2, Citation: "Section 4", ParentCitation: "Section 4", Score: 0.014},
			{ChunkID: 50, DocumentID: 2, Citation: "Section 5", ParentCitation: "Section 5", Score: 0.005},
			{ChunkID: 60, DocumentID: 2, Citation: "Section 6", ParentCitation: "Section 6", Score: 0.004},
			{ChunkID: 70, DocumentID: 2, Citation: "Section 7", ParentCitation: "Section 7", Score: 0.003},
			{ChunkID: 80, DocumentID: 2, Citation: "Section 8", ParentCitation: "Section 8", Score: 0.002},
			{ChunkID: 101, DocumentID: 1, Citation: "Section 22, (1)", ParentCitation: "Section 22, (1)", Score: 0.011},
			{ChunkID: 102, DocumentID: 1, Citation: "Section 22, (2)", ParentCitation: "Section 22, (2)", Score: 0.010},
		}
		got := aggMY(hits, 3)
		// Find the promoted Section 22 entry.
		found := false
		for _, h := range got {
			if h.ChunkID == 101 {
				found = true
				if h.ParentCitation != "Section 22, (1)" {
					t.Errorf("ParentCitation = %q, want preserved", h.ParentCitation)
				}
				break
			}
		}
		if !found {
			t.Error("Section 22 best member not found in output")
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
			{"Chapter II", "section ", "Chapter II"},
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

	t.Run("group already in window not promoted again", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Section 1, (1)", 0.020, 11),
			hit(1, "Section 1, (2)", 0.018, 12),
			hit(2, "Section 3", 0.016, 30),
			hit(2, "Section 4", 0.014, 40),
			hit(1, "Section 1, (3)", 0.010, 13),
			hit(2, "Section 5", 0.006, 50),
		}
		got := aggMY(hits, 3)
		// Section 1 is already in the top-k; no promotion.
		if len(got) != len(hits) {
			t.Fatalf("len = %d, want %d (no promotion expected)", len(got), len(hits))
		}
	})

	t.Run("candidate must beat weakest top-k to promote", func(t *testing.T) {
		hits := []Hit{
			hit(1, "Section 1", 0.020, 10),
			hit(1, "Section 2", 0.015, 20),
			hit(2, "Section 3", 0.012, 30),
			hit(2, "Section 4", 0.010, 40),
			hit(2, "Section 5", 0.009, 50),
			hit(2, "Section 6", 0.008, 60),
			hit(2, "Section 7", 0.007, 70),
			hit(2, "Section 8", 0.005, 80),
			hit(1, "Section 99, (1)", 0.002, 991),
			hit(1, "Section 99, (2)", 0.001, 992),
		}
		got := aggMY(hits, 3)
		// Aggregate of Section 99 = 0.003 < 0.005 (weakest). No promotion.
		if len(got) != 8 {
			t.Fatalf("len = %d, want 8 (no promotion)", len(got))
		}
	})

	t.Run("maxPromote caps appended entries", func(t *testing.T) {
		// Three multi-fragment groups outside the window, but maxPromote=2.
		hits := []Hit{
			hit(1, "Section 1", 0.020, 10),
			hit(1, "Section 2", 0.015, 20),
			hit(2, "Section 3", 0.012, 30),
			hit(2, "Section 4", 0.010, 40),
			hit(2, "Section 5", 0.006, 50),
			hit(2, "Section 6", 0.005, 60),
			hit(2, "Section 7", 0.004, 70),
			hit(2, "Section 8", 0.003, 80),
			hit(1, "Section 22, (1)", 0.009, 201),
			hit(1, "Section 22, (2)", 0.008, 202),
			hit(3, "Section 33, (1)", 0.009, 301),
			hit(3, "Section 33, (2)", 0.008, 302),
			hit(4, "Section 44, (1)", 0.009, 401),
			hit(4, "Section 44, (2)", 0.008, 402),
		}
		got := aggMY(hits, 3)
		// Natural top-k (8) + at most 2 promoted = 10.
		if len(got) > 10 {
			t.Errorf("len = %d, want <=10 (maxPromote=2)", len(got))
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
