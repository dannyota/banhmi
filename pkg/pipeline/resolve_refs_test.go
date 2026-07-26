package pipeline

import "testing"

// TestRefResolutionCandidates covers the two shapes doc_ref keys actually take:
// Vietnamese keys are already bare numbers, Indonesian ones are whole source
// sentences with the number embedded and a doc-type prefix to recover.
func TestRefResolutionCandidates(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
		want string // the candidate that must be produced (first match wins)
	}{
		{"vn bare number", "24/2018/QH14", "24/2018/QH14"},
		{"vn circular", "22/2020/TT-BTTTT", "22/2020/TT-BTTTT"},
		{"id bare sector number", "21/9/PBI/2019", "PBI21/9/PBI/2019"},
		{"id verbose with title", "PERATURAN OTORITAS JASA KEUANGAN NOMOR 31/POJK.07/2020 TENTANG PENYELENGGARAAN LAYANAN", "POJK31/POJK.07/2020"},
		{"id verbose bank indonesia", "PERATURAN BANK INDONESIA NOMOR 14/27/PBI/2012", "PBI14/27/PBI/2012"},
		{"id spaced slashes", "PBI NOMOR 34 /POJK.04/ 2019", "PBI34/POJK.04/2019"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := refResolutionCandidates(tc.key)
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
	got := refResolutionCandidates("PERATURAN NOMOR 40/POJK.04/2024 TENTANG X")
	if len(got) == 0 {
		t.Fatal("no candidates")
	}
	want := normalizeDocNumberForStorage("POJK40/POJK.04/2024")
	if got[0] != want {
		t.Errorf("first candidate = %q, want %q (all: %v)", got[0], want, got)
	}
}
