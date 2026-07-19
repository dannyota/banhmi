package retrieve

import "testing"

func TestParentCitation(t *testing.T) {
	tests := []struct {
		citation, level, articlePfx, subArticlePfx, want string
	}{
		{"Điều 7, Khoản 2, Điểm a", rollupKhoan, "điều ", "khoản ", "Điều 7, Khoản 2"},
		{"Điều 7, Khoản 1", rollupKhoan, "điều ", "khoản ", "Điều 7, Khoản 1"},
		{"Điều 19", rollupKhoan, "điều ", "khoản ", "Điều 19"}, // no Khoản → unchanged
		{"Điều 22, Khoản 2, Điểm d, Đoạn 1", rollupKhoan, "điều ", "khoản ", "Điều 22, Khoản 2"},
		{"Điều 7, Khoản 2, Điểm a", rollupDieu, "điều ", "khoản ", "Điều 7"},
		{"Điều 7, Khoản 6, Đoạn 3", rollupDieu, "điều ", "khoản ", "Điều 7"},
		{"Điều 7, Khoản 2", rollupNone, "điều ", "khoản ", "Điều 7, Khoản 2"}, // none → unchanged
		// MY: Section/Subsection
		{"Section 47, (2), (b), Paragraph 1", rollupKhoan, "section ", "subsection ", "Section 47, (2), (b), Paragraph 1"}, // no "subsection " prefix → unchanged
		{"Section 47, Subsection 2, (a)", rollupKhoan, "section ", "subsection ", "Section 47, Subsection 2"},
		{"Section 47, Subsection 2, (a)", rollupDieu, "section ", "subsection ", "Section 47"},
		// ID: Pasal/Ayat
		{"Pasal 12, Ayat (3), Huruf b", rollupKhoan, "pasal ", "ayat ", "Pasal 12, Ayat (3)"},
		{"Pasal 12, Ayat (3), Huruf b", rollupDieu, "pasal ", "ayat ", "Pasal 12"},
	}
	for _, tt := range tests {
		if got := parentCitation(tt.citation, tt.level, tt.articlePfx, tt.subArticlePfx); got != tt.want {
			t.Errorf("parentCitation(%q, %q) = %q, want %q", tt.citation, tt.level, got, tt.want)
		}
	}
}

func TestRollupByParent(t *testing.T) {
	ap, sap := "điều ", "khoản "
	// Four hits from doc 1: three Điểm of Điều 18 Khoản 2 plus Điều 17 Khoản 2.
	hits := []Hit{
		{DocumentID: 1, Citation: "Điều 18, Khoản 2, Điểm a", Score: 0.9},
		{DocumentID: 1, Citation: "Điều 17, Khoản 2", Score: 0.8},
		{DocumentID: 1, Citation: "Điều 18, Khoản 2, Điểm h", Score: 0.7},
		{DocumentID: 1, Citation: "Điều 18, Khoản 2, Điểm l", Score: 0.6},
	}

	got := rollupByParent(hits, rollupKhoan, ap, sap)
	// Điều 18 Khoản 2's three Điểm collapse to the first (best) one; Điều 17 Khoản 2 stays.
	if len(got) != 2 {
		t.Fatalf("rolled-up hits = %d, want 2", len(got))
	}
	if got[0].Citation != "Điều 18, Khoản 2, Điểm a" || got[0].ParentCitation != "Điều 18, Khoản 2" {
		t.Errorf("hit0 = %q (parent %q), want best Điểm a with parent Điều 18, Khoản 2", got[0].Citation, got[0].ParentCitation)
	}
	if got[1].Citation != "Điều 17, Khoản 2" {
		t.Errorf("hit1 = %q, want Điều 17, Khoản 2", got[1].Citation)
	}

	// Same parent citation but different documents must NOT collapse.
	cross := []Hit{
		{DocumentID: 1, Citation: "Điều 1, Khoản 1, Điểm a"},
		{DocumentID: 2, Citation: "Điều 1, Khoản 1, Điểm b"},
	}
	if got := rollupByParent(cross, rollupKhoan, ap, sap); len(got) != 2 {
		t.Fatalf("cross-document roll-up = %d, want 2 (different docs)", len(got))
	}

	// level none keeps every hit but still annotates ParentCitation.
	none := rollupByParent(hits, rollupNone, ap, sap)
	if len(none) != len(hits) {
		t.Fatalf("rollupNone hits = %d, want %d", len(none), len(hits))
	}
	if none[0].ParentCitation != "Điều 18, Khoản 2, Điểm a" {
		t.Errorf("rollupNone parent = %q, want citation unchanged", none[0].ParentCitation)
	}
}
