package retrieve

import "testing"

func TestCapPerDocument(t *testing.T) {
	h := func(doc int64) Hit { return Hit{DocumentID: doc} }
	docsOf := func(hits []Hit) []int64 {
		out := make([]int64, len(hits))
		for i, x := range hits {
			out[i] = x.DocumentID
		}
		return out
	}
	// One document crowding all slots: cap frees slots for later documents.
	in := []Hit{h(1), h(1), h(1), h(1), h(1), h(2), h(3), h(1), h(4), h(5)}
	got := capPerDocument(in, 3, 8)
	want := []int64{1, 1, 1, 2, 3, 4, 5, 1} // 3 from doc 1, then others, backfill demoted doc-1 hit
	for i, d := range want {
		if got[i].DocumentID != d {
			t.Fatalf("capped order = %v, want %v", docsOf(got), want)
		}
	}
	// Cap never shrinks the result below the uncapped truncation.
	if len(got) != 8 {
		t.Fatalf("len = %d, want 8", len(got))
	}
	// No crowding: cap is a no-op.
	in2 := []Hit{h(1), h(2), h(3)}
	got2 := capPerDocument(in2, 3, 8)
	if len(got2) != 3 || got2[0].DocumentID != 1 || got2[2].DocumentID != 3 {
		t.Fatalf("no-op case changed hits: %v", docsOf(got2))
	}
	// Zero topK: unchanged input.
	if out := capPerDocument(in2, 2, 0); len(out) != 3 {
		t.Fatalf("topK=0 should return input unchanged")
	}
}
