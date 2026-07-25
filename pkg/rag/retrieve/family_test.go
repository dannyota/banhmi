package retrieve

import (
	"reflect"
	"testing"
)

// A consolidation and its base citing the same provision collapse to the
// better-ranked hit; different provisions and out-of-family documents pass
// through, and an empty family map is a no-op.
func TestCollapseFamilyDuplicates(t *testing.T) {
	// Family: consolidation 10 → base 20 (root 10); document 99 is unrelated.
	fam := BuildFamilyMap([][2]int64{{10, 20}})

	hits := []Hit{
		{ChunkID: 1, DocumentID: 10, Citation: "Điều 18, Khoản 3"},
		{ChunkID: 2, DocumentID: 99, Citation: "Điều 18, Khoản 3"},
		{ChunkID: 3, DocumentID: 20, Citation: "Điều 18, Khoản 3"}, // twin of chunk 1 — dropped
		{ChunkID: 4, DocumentID: 20, Citation: "Điều 5"},           // same family, other provision — kept
	}
	got := collapseFamilyDuplicates(hits, fam)
	var ids []int64
	for _, h := range got {
		ids = append(ids, h.ChunkID)
	}
	if want := []int64{1, 2, 4}; !reflect.DeepEqual(ids, want) {
		t.Errorf("collapsed chunk ids = %v, want %v", ids, want)
	}

	if got := collapseFamilyDuplicates(hits, nil); len(got) != len(hits) {
		t.Errorf("empty family map must be a no-op, got %d of %d hits", len(got), len(hits))
	}
}

// The family root is the smallest document id in the connected component, and
// components merge across shared members (two consolidations of one base, and
// a folded-in amendment, all land in one family).
func TestBuildFamilyMap(t *testing.T) {
	pairs := [][2]int64{
		{30, 7},  // consolidation 30 → base 7
		{40, 7},  // consolidation 40 → same base
		{30, 25}, // consolidation 30 → folded amendment 25
		{90, 80}, // unrelated family
	}
	m := BuildFamilyMap(pairs)
	for _, id := range []int64{30, 40, 7, 25} {
		if m[id] != 7 {
			t.Errorf("family root of %d = %d, want 7", id, m[id])
		}
	}
	for _, id := range []int64{90, 80} {
		if m[id] != 80 {
			t.Errorf("family root of %d = %d, want 80", id, m[id])
		}
	}
	if _, ok := m[999]; ok {
		t.Error("document outside any pair must be absent from the map")
	}
}
