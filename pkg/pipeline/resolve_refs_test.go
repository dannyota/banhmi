package pipeline

import (
	"testing"

	"danny.vn/banhmi/pkg/base/jurisdiction"
)

// TestRefResolutionCandidates covers the two shapes doc_ref keys actually take:
// Vietnamese keys are already bare numbers, Indonesian ones are whole source
// sentences with the number embedded and a doc-type prefix to recover.
func TestRefResolutionCandidates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		canon string
		key   string
		want  string // the candidate that must be produced
	}{
		{"vn bare number", jurisdiction.RefCanonDefault, "24/2018/QH14", "24/2018/QH14"},
		{"vn circular", jurisdiction.RefCanonDefault, "22/2020/TT-BTTTT", "22/2020/TT-BTTTT"},
		{"unknown key falls back to default", "", "24/2018/QH14", "24/2018/QH14"},
		{"id bare sector number", jurisdiction.RefCanonIDForms, "21/9/PBI/2019", "PBI21/9/PBI/2019"},
		{"id verbose with title", jurisdiction.RefCanonIDForms, "PERATURAN OTORITAS JASA KEUANGAN NOMOR 31/POJK.07/2020 TENTANG PENYELENGGARAAN LAYANAN", "POJK31/POJK.07/2020"},
		{"id verbose bank indonesia", jurisdiction.RefCanonIDForms, "PERATURAN BANK INDONESIA NOMOR 14/27/PBI/2012", "PBI14/27/PBI/2012"},
		{"id spaced slashes", jurisdiction.RefCanonIDForms, "PBI NOMOR 34 /POJK.04/ 2019", "PBI34/POJK.04/2019"},
		{"id filler words folded", jurisdiction.RefCanonIDForms, "PADG NO.11 TAHUN 2024", "PADG 11/2024"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := refCanonicalizerFor(tc.canon)(tc.key)
			want := normalizeDocNumberForStorage(tc.want)
			for _, c := range got {
				if c == want {
					return
				}
			}
			t.Errorf("candidates %v do not contain %q (from %q)", got, want, tc.want)
		})
	}
}

// TestRefResolutionCandidatesPrefersSectorForm guards the ordering: a bare
// sector-coded number also satisfies the looser slash pattern, so the sector
// regex must run first or the wrong span is captured.
func TestRefResolutionCandidatesPrefersSectorForm(t *testing.T) {
	got := idRefCandidates("PERATURAN NOMOR 40/POJK.04/2024 TENTANG X")
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	want := normalizeDocNumberForStorage("POJK40/POJK.04/2024")
	if got[0] != want {
		t.Errorf("first candidate = %q, want %q (all: %v)", got[0], want, got)
	}
}

// TestDefaultRefCandidatesIgnoresIndonesianForms pins the per-country split: the
// default canonicaliser must NOT apply Indonesian folding, so a country that has
// not opted in cannot have its keys silently rewritten.
func TestDefaultRefCandidatesIgnoresIndonesianForms(t *testing.T) {
	got := defaultRefCandidates("PERATURAN OTORITAS JASA KEUANGAN NOMOR 31/POJK.07/2020 TENTANG X")
	if len(got) != 1 {
		t.Fatalf("default canonicaliser produced %d candidates, want 1: %v", len(got), got)
	}
	if id := idRefCandidates("PERATURAN OTORITAS JASA KEUANGAN NOMOR 31/POJK.07/2020 TENTANG X"); len(id) <= len(got) {
		t.Errorf("ID canonicaliser should produce more candidates than default; got %v vs %v", id, got)
	}
}
