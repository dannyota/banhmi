package retrieve

import "sort"

// ranked is one chunk id at a 1-based position in a single arm's result list.
// The vector and lexical arms each produce a []ranked, which fuseRRF combines.
type ranked struct {
	chunkID int64
	rank    int // 1-based: best result is rank 1
}

// fusedHit is the output of RRF fusion: a chunk id and its summed RRF score,
// plus the per-arm 1-based ranks (0 = absent from that arm) for diagnostics.
type fusedHit struct {
	chunkID    int64
	score      float64
	vectorRank int
	bm25Rank   int
}

// fuseRRF combines two ranked lists with Reciprocal Rank Fusion:
//
//	score(d) = Σ_arm 1 / (rrfK + rank_arm(d))
//
// A chunk present in both arms accumulates both terms. rrfK damps the influence of
// top ranks (the standard constant is 60); it must be > 0 to avoid division issues
// — callers pass cfg.Retrieve.RRFK, and Search clamps a non-positive value first.
//
// The result is sorted by score descending; ties break by chunk id ascending so
// the output is fully deterministic (important for tests and reproducible answers).
// vectorList is treated as the first arm and bm25List as the second purely for the
// per-arm rank bookkeeping; fusion itself is symmetric.
func fuseRRF(vectorList, bm25List []ranked, rrfK int) []fusedHit {
	if rrfK <= 0 {
		rrfK = defaultRRFK
	}
	k := float64(rrfK)

	byID := make(map[int64]*fusedHit)
	get := func(id int64) *fusedHit {
		h, ok := byID[id]
		if !ok {
			h = &fusedHit{chunkID: id}
			byID[id] = h
		}
		return h
	}

	for _, r := range vectorList {
		if r.rank <= 0 {
			continue
		}
		h := get(r.chunkID)
		h.score += 1.0 / (k + float64(r.rank))
		if h.vectorRank == 0 || r.rank < h.vectorRank {
			h.vectorRank = r.rank
		}
	}
	for _, r := range bm25List {
		if r.rank <= 0 {
			continue
		}
		h := get(r.chunkID)
		h.score += 1.0 / (k + float64(r.rank))
		if h.bm25Rank == 0 || r.rank < h.bm25Rank {
			h.bm25Rank = r.rank
		}
	}

	if len(byID) == 0 {
		return nil
	}
	out := make([]fusedHit, 0, len(byID))
	for _, h := range byID {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].chunkID < out[j].chunkID
	})
	return out
}
