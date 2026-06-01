package retrieve

import (
	"math"
	"testing"
)

const eps = 1e-9

func approxEq(a, b float64) bool { return math.Abs(a-b) < eps }

// rrf is a tiny helper mirroring the scoring formula for expected values.
func rrf(k, rank int) float64 { return 1.0 / (float64(k) + float64(rank)) }

func TestFuseRRF_singleArm(t *testing.T) {
	// BM25-only: ranks 1,2,3 → scores strictly decreasing, order preserved.
	bm25 := []ranked{{10, 1}, {20, 2}, {30, 3}}
	got := fuseRRF(nil, bm25, 60)

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	wantOrder := []int64{10, 20, 30}
	for i, h := range got {
		if h.chunkID != wantOrder[i] {
			t.Errorf("pos %d: chunkID = %d, want %d", i, h.chunkID, wantOrder[i])
		}
		if want := rrf(60, i+1); !approxEq(h.score, want) {
			t.Errorf("pos %d: score = %v, want %v", i, h.score, want)
		}
		if h.bm25Rank != i+1 {
			t.Errorf("pos %d: bm25Rank = %d, want %d", i, h.bm25Rank, i+1)
		}
		if h.vectorRank != 0 {
			t.Errorf("pos %d: vectorRank = %d, want 0", i, h.vectorRank)
		}
	}
}

func TestFuseRRF_overlapBoostsScore(t *testing.T) {
	// id 20 appears in both arms; it must outrank ids that appear in only one,
	// even when those hold rank 1 in their single arm — the standard RRF result
	// at k=60 (1/61 + 1/62 ≈ 0.03252 > 1/61 ≈ 0.01639).
	vector := []ranked{{10, 1}, {20, 2}}
	bm25 := []ranked{{30, 1}, {20, 2}}
	got := fuseRRF(vector, bm25, 60)

	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (deduped union)", len(got))
	}
	if got[0].chunkID != 20 {
		t.Fatalf("top chunkID = %d, want 20 (present in both arms)", got[0].chunkID)
	}

	wantTop := rrf(60, 2) + rrf(60, 2)
	if !approxEq(got[0].score, wantTop) {
		t.Errorf("top score = %v, want %v", got[0].score, wantTop)
	}
	if got[0].vectorRank != 2 || got[0].bm25Rank != 2 {
		t.Errorf("top ranks = (v=%d,b=%d), want (2,2)", got[0].vectorRank, got[0].bm25Rank)
	}

	// 10 (vector rank 1) and 30 (bm25 rank 1) tie on score → id ascending.
	if got[1].chunkID != 10 || got[2].chunkID != 30 {
		t.Errorf("tie order = [%d, %d], want [10, 30]", got[1].chunkID, got[2].chunkID)
	}
	if !approxEq(got[1].score, got[2].score) {
		t.Errorf("tied scores differ: %v vs %v", got[1].score, got[2].score)
	}
}

func TestFuseRRF_dedupKeepsBestRankPerArm(t *testing.T) {
	// A duplicate id within one arm (shouldn't happen from SQL, but be safe):
	// keep the best (smallest) rank, and the score reflects both contributions.
	bm25 := []ranked{{5, 3}, {5, 1}}
	got := fuseRRF(nil, bm25, 60)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].bm25Rank != 1 {
		t.Errorf("bm25Rank = %d, want 1 (best kept)", got[0].bm25Rank)
	}
	want := rrf(60, 3) + rrf(60, 1)
	if !approxEq(got[0].score, want) {
		t.Errorf("score = %v, want %v (both contributions summed)", got[0].score, want)
	}
}

func TestFuseRRF_empty(t *testing.T) {
	if got := fuseRRF(nil, nil, 60); got != nil {
		t.Errorf("fuseRRF(nil,nil) = %v, want nil", got)
	}
}

func TestFuseRRF_zeroRanksIgnored(t *testing.T) {
	// rank 0 means "absent from this arm" and must contribute nothing.
	got := fuseRRF([]ranked{{1, 0}}, []ranked{{2, 1}}, 60)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (rank-0 entry dropped)", len(got))
	}
	if got[0].chunkID != 2 {
		t.Errorf("chunkID = %d, want 2", got[0].chunkID)
	}
}

func TestFuseRRF_nonPositiveKFallsBack(t *testing.T) {
	// k<=0 must fall back to the default constant, not divide oddly.
	got := fuseRRF(nil, []ranked{{1, 1}}, 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if want := rrf(defaultRRFK, 1); !approxEq(got[0].score, want) {
		t.Errorf("score = %v, want %v (default k=%d)", got[0].score, want, defaultRRFK)
	}
}

func TestFuseRRF_deterministicTieBreak(t *testing.T) {
	// All four ids tie on score (each rank 1 in exactly one arm, fed in shuffled
	// order). Output must be strictly id-ascending every time.
	vector := []ranked{{40, 1}, {10, 1}}
	bm25 := []ranked{{30, 1}, {20, 1}}
	got := fuseRRF(vector, bm25, 60)
	want := []int64{10, 20, 30, 40}
	for i, h := range got {
		if h.chunkID != want[i] {
			t.Fatalf("pos %d: chunkID = %d, want %d (tie → id asc)", i, h.chunkID, want[i])
		}
	}
}
