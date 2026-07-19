package main

import "testing"

func TestRerankAndCap(t *testing.T) {
	cands := []rerankCandidate{
		{ChunkID: 1, DocumentID: 10, DocNumber: "A", Citation: "Điều 1"},
		{ChunkID: 2, DocumentID: 10, DocNumber: "A", Citation: "Điều 2"},
		{ChunkID: 3, DocumentID: 10, DocNumber: "A", Citation: "Điều 3"},
		{ChunkID: 4, DocumentID: 20, DocNumber: "B", Citation: "Điều 9"},
		{ChunkID: 5, DocumentID: 10, DocNumber: "A", Citation: "Điều 4"},
	}
	// Scores put all of doc A first; the cap must let doc B in.
	scores := []float64{0.9, 0.8, 0.7, 0.1, 0.6}
	hits := rerankAndCap(cands, scores, 2, 3)
	if len(hits) != 3 {
		t.Fatalf("len = %d, want 3", len(hits))
	}
	if hits[0].ChunkID != 1 || hits[1].ChunkID != 2 {
		t.Fatalf("top-2 = %d,%d want 1,2", hits[0].ChunkID, hits[1].ChunkID)
	}
	if hits[2].ChunkID != 4 {
		t.Fatalf("third = %d, want 4 (doc cap must skip chunk 3)", hits[2].ChunkID)
	}

	// Short score slice: unscored candidates rank last, never panic.
	hits = rerankAndCap(cands, []float64{0.1, 0.9}, 0, 5)
	if hits[0].ChunkID != 2 {
		t.Fatalf("top = %d, want 2", hits[0].ChunkID)
	}
	if len(hits) != 5 {
		t.Fatalf("len = %d, want 5 (docCap 0 disables)", len(hits))
	}
}
