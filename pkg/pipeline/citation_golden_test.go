package pipeline

import (
	"fmt"
	"testing"

	dbsilver "danny.vn/banhmi/pkg/store/silver"
)

// Golden regression guard for the VN citation strings written into
// gold.chunk.citation. The multi-jurisdiction seam will move the provision
// labels (Phần/Chương/Mục/Điều/Khoản/Điểm) into config; this test locks the
// EXACT current output so that refactor keeps gold.chunk.citation
// byte-identical — the invariant that lets the live VN corpus skip
// re-chunking/re-embedding. Do not "fix" these expectations: a change here is
// a corpus-affecting change and must be deliberate (and paired with a re-index).

func TestSectionCitationPartGolden(t *testing.T) {
	cases := []struct{ name, kind, label, want string }{
		{"chuong from numbered label", "chuong", "I.", "Chương I"},
		{"chuong already prefixed", "chuong", "Chương I", "Chương I"},
		{"muc letter", "muc", "A.", "Mục A"},
		{"dieu numbered with trailing dot", "dieu", "7.", "Điều 7"},
		{"dieu already prefixed", "dieu", "Điều 7", "Điều 7"},
		{"khoan numbered", "khoan", "2.", "Khoản 2"},
		{"diem letter trims paren", "diem", "a)", "Điểm a"},
		{"diem vietnamese letter", "diem", "đ)", "Điểm đ"},
		{"phuluc passthrough (no prefix)", "phuluc", "Phụ lục I", "Phụ lục I"},
		{"phan passthrough (no prefix)", "phan", "Phần 1", "Phần 1"},
		// ID provision kinds
		{"pasal numbered", "pasal", "26", "Pasal 26"},
		{"pasal already prefixed", "pasal", "Pasal 7", "Pasal 7"},
		{"ayat numbered", "ayat", "1", "ayat (1)"},
		{"ayat parser-wrapped label", "ayat", "ayat (1)", "ayat (1)"},
		{"huruf letter", "huruf", "a", "huruf a"},
		{"huruf parser-prefixed label", "huruf", "huruf a", "huruf a"},
		{"bab passthrough", "bab", "BAB IV", "BAB IV"},
		{"bagian passthrough", "bagian", "Bagian Kesatu", "Bagian Kesatu"},
		{"paragraf passthrough", "paragraf", "Paragraf 1", "Paragraf 1"},
		{"penjelasan passthrough", "penjelasan", "Penjelasan Umum", "Penjelasan Umum"},
		{"lampiran passthrough", "lampiran", "Lampiran I", "Lampiran I"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sec := makeSection(1, nil, tc.kind, 1, tc.label, "", "", tc.kind+"-x")
			if got := sectionCitationPart(&sec); got != tc.want {
				t.Fatalf("sectionCitationPart(kind=%s label=%q) = %q, want %q", tc.kind, tc.label, got, tc.want)
			}
		})
	}
}

func TestSectionCitationChainGolden(t *testing.T) {
	cite := func(secs ...dbsilver.SilverDocumentSection) string {
		byID := make(map[int64]*dbsilver.SilverDocumentSection, len(secs))
		for i := range secs {
			byID[secs[i].ID] = &secs[i]
		}
		return sectionCitation(&secs[len(secs)-1], byID)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{
			"chuong/muc/khoan",
			cite(
				makeSection(1, nil, "chuong", 1, "I.", "", "", "chuong-I"),
				makeSection(2, sectionID(1), "muc", 1, "A.", "", "", "chuong-I/muc-A"),
				makeSection(3, sectionID(2), "khoan", 1, "1.", "", "", "chuong-I/muc-A/khoan-1"),
			),
			"Chương I, Mục A, Khoản 1",
		},
		{
			"dieu/khoan/diem",
			cite(
				makeSection(1, nil, "dieu", 7, "Điều 7", "", "", "dieu-7"),
				makeSection(2, sectionID(1), "khoan", 2, "2.", "", "", "dieu-7/khoan-2"),
				makeSection(3, sectionID(2), "diem", 1, "a)", "", "", "dieu-7/khoan-2/diem-a"),
			),
			"Điều 7, Khoản 2, Điểm a",
		},
		{
			"phuluc/dieu",
			cite(
				makeSection(1, nil, "phuluc", 1, "Phụ lục I", "", "", "phuluc-I"),
				makeSection(2, sectionID(1), "dieu", 3, "3.", "", "", "phuluc-I/dieu-3"),
			),
			"Phụ lục I, Điều 3",
		},
		// ID citation chains
		{
			"bab/bagian/pasal",
			cite(
				makeSection(1, nil, "bab", 1, "BAB IV", "Hak Subjek Data Pribadi", "", "bab-IV"),
				makeSection(2, sectionID(1), "bagian", 1, "Bagian Kesatu", "Umum", "", "bab-IV/bagian-1"),
				makeSection(3, sectionID(2), "pasal", 5, "5", "", "", "bab-IV/bagian-1/pasal-5"),
			),
			"BAB IV, Bagian Kesatu, Pasal 5",
		},
		{
			"pasal/ayat/huruf",
			cite(
				makeSection(1, nil, "pasal", 26, "26", "", "", "pasal-26"),
				makeSection(2, sectionID(1), "ayat", 1, "1", "", "", "pasal-26/ayat-1"),
				makeSection(3, sectionID(2), "huruf", 1, "a", "", "", "pasal-26/ayat-1/huruf-a"),
			),
			"Pasal 26, ayat (1), huruf a",
		},
		{
			// Labels exactly as pkg/pipeline/indonesiaparse.go stores them —
			// pre-wrapped ("Pasal 26", "ayat (1)", "huruf a"). Guards the
			// double-wrap regression ("ayat (ayat (1)", "huruf huruf a").
			"pasal/ayat/huruf parser labels",
			cite(
				makeSection(1, nil, "pasal", 26, "Pasal 26", "", "", "pasal-26"),
				makeSection(2, sectionID(1), "ayat", 1, "ayat (1)", "", "", "pasal-26/ayat-1"),
				makeSection(3, sectionID(2), "huruf", 1, "huruf a", "", "", "pasal-26/ayat-1/huruf-a"),
			),
			"Pasal 26, ayat (1), huruf a",
		},
		{
			"lampiran/pasal",
			cite(
				makeSection(1, nil, "lampiran", 1, "Lampiran I", "", "", "lampiran-I"),
				makeSection(2, sectionID(1), "pasal", 3, "3", "", "", "lampiran-I/pasal-3"),
			),
			"Lampiran I, Pasal 3",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("sectionCitation = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestVNCitationDedupDisambiguation verifies that VN sections with duplicate
// labels (e.g. amendment decrees where Điều 1 has many Khoản all labelled "1.",
// or tabular documents with many Điểm all labelled "i)") get unique citations
// via the native ", Đoạn N" suffix. The parser's uniqueCitationPath appends
// "~2", "~3", … to the citation_path; sectionCitationPart must surface this
// as ", Đoạn N" so no two sibling chunks share a citation string.
func TestVNCitationDedupDisambiguation(t *testing.T) {
	t.Run("sectionCitationPart with VN dedup suffix", func(t *testing.T) {
		cases := []struct {
			name    string
			kind    string
			label   string
			citPath string
			want    string
		}{
			// No dedup suffix — citation unchanged.
			{"khoan no dedup", "khoan", "1.", "dieu-1/khoan-1", "Khoản 1"},
			{"dieu no dedup", "dieu", "Điều 7", "dieu-7", "Điều 7"},
			{"diem no dedup", "diem", "a)", "dieu-1/khoan-1/diem-a", "Điểm a"},
			{"chuong no dedup", "chuong", "I.", "chuong-I", "Chương I"},
			{"muc no dedup", "muc", "1", "muc-1", "Mục 1"},
			// Dedup suffix — Đoạn appended.
			{"khoan dedup 2", "khoan", "1.", "dieu-1/khoan-1~2", "Khoản 1, Đoạn 2"},
			{"khoan dedup 21", "khoan", "1.", "dieu-1/khoan-1~21", "Khoản 1, Đoạn 21"},
			{"diem dedup 3", "diem", "i)", "dieu-2/khoan-1/diem-i~3", "Điểm i, Đoạn 3"},
			{"diem dedup 12", "diem", "i)", "dieu-2/khoan-1/diem-i~12", "Điểm i, Đoạn 12"},
			{"dieu dedup 2", "dieu", "Điều 5", "phuluc/dieu-5~2", "Điều 5, Đoạn 2"},
			{"chuong dedup", "chuong", "I.", "chuong-I~3", "Chương I, Đoạn 3"},
			{"muc dedup", "muc", "1", "muc-1~2", "Mục 1, Đoạn 2"},
			// MY/ID kinds are unaffected (they use ~ in VN context but shouldn't
			// happen in practice since these kinds belong to MY/ID parsers).
			{"pasal no dedup", "pasal", "26", "pasal-26", "Pasal 26"},
			{"section no dedup", "section", "Section 5", "section-5", "Section 5"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				sec := makeSection(1, nil, tc.kind, 1, tc.label, "", "", tc.citPath)
				got := sectionCitationPart(&sec)
				if got != tc.want {
					t.Fatalf("sectionCitationPart(kind=%s label=%q citPath=%q) = %q, want %q",
						tc.kind, tc.label, tc.citPath, got, tc.want)
				}
			})
		}
	})

	// Full chain: amendment decree style — Điều 1, Khoản 1 through Khoản 1~21.
	t.Run("143/2021 amendment decree unique citations", func(t *testing.T) {
		byID := make(map[int64]*dbsilver.SilverDocumentSection)
		dieu := makeSection(1, nil, "dieu", 1, "Điều 1", "Sửa đổi, bổ sung", "", "dieu-1")
		byID[1] = &dieu

		seen := make(map[string]bool)
		for i := 1; i <= 21; i++ {
			var path string
			if i == 1 {
				path = "dieu-1/khoan-1"
			} else {
				path = fmt.Sprintf("dieu-1/khoan-1~%d", i)
			}
			k := makeSection(int64(1+i), sectionID(1), "khoan", 1, "1.", "", "", path)
			byID[k.ID] = &k
			cite := sectionCitation(&k, byID)
			if seen[cite] {
				t.Fatalf("duplicate citation at instance %d: %q", i, cite)
			}
			seen[cite] = true
		}
		if len(seen) != 21 {
			t.Fatalf("expected 21 unique citations, got %d", len(seen))
		}
	})

	// Tabular document — many Điểm "i)" under one Khoản, each with dedup suffix.
	t.Run("1089/QD-NHNN tabular diem unique citations", func(t *testing.T) {
		byID := make(map[int64]*dbsilver.SilverDocumentSection)
		dieu := makeSection(1, nil, "dieu", 2, "Điều 2", "", "", "dieu-2")
		byID[1] = &dieu
		khoan := makeSection(2, sectionID(1), "khoan", 1, "1.", "", "", "dieu-2/khoan-1~4")
		byID[2] = &khoan

		seen := make(map[string]bool)
		for i := 1; i <= 12; i++ {
			var path string
			if i == 1 {
				path = "dieu-2/khoan-1~4/diem-i"
			} else {
				path = fmt.Sprintf("dieu-2/khoan-1~4/diem-i~%d", i)
			}
			d := makeSection(int64(2+i), sectionID(2), "diem", 9, "i)", "", "", path)
			byID[d.ID] = &d
			cite := sectionCitation(&d, byID)
			if seen[cite] {
				t.Fatalf("duplicate citation at diem instance %d: %q", i, cite)
			}
			seen[cite] = true
		}
		if len(seen) != 12 {
			t.Fatalf("expected 12 unique citations, got %d", len(seen))
		}
	})
}

// TestMYCitationDedupDisambiguation verifies that MY/SG definition sections
// whose (a)/(b)/… paragraphs restart per defined term produce unique, stable
// citations. The parser's uniqueSeg dedup generates citation_path segments like
// "paragraph-a", "paragraph-a-2", …, "paragraph-a-24"; sectionCitationPart must
// surface the dedup ordinal so no two sibling chunks share a citation string.
// This is the regression guard for the Act 758 Section 2 collision fix.
func TestMYCitationDedupDisambiguation(t *testing.T) {
	t.Run("sectionCitationPart", func(t *testing.T) {
		cases := []struct {
			name    string
			kind    string
			label   string
			citPath string
			want    string
		}{
			// No dedup suffix — citation unchanged.
			{"paragraph no dedup", "paragraph", "(a)", "section-2/subsection-1/paragraph-a", "(a)"},
			{"subsection no dedup", "subsection", "(1)", "section-2/subsection-1", "(1)"},
			{"section no dedup", "section", "Section 14", "section-14", "Section 14"},
			{"section with letter suffix", "section", "Section 14A", "section-14a", "Section 14A"},
			// Dedup suffix — ordinal appended.
			{"paragraph-a dedup 2", "paragraph", "(a)", "section-2/subsection-1/paragraph-a-2", "(a) [2]"},
			{"paragraph-a dedup 13", "paragraph", "(a)", "section-2/subsection-1/paragraph-a-13", "(a) [13]"},
			{"paragraph-a dedup 24", "paragraph", "(a)", "section-2/subsection-1/paragraph-a-24", "(a) [24]"},
			{"paragraph-b dedup", "paragraph", "(b)", "section-2/subsection-1/paragraph-b-3", "(b) [3]"},
			{"paragraph-aa dedup", "paragraph", "(aa)", "section-2/subsection-1/paragraph-aa-2", "(aa) [2]"},
			// Subparagraph dedup.
			{"subparagraph-i dedup", "paragraph", "(i)", "section-5/subsection-2/paragraph-c/subparagraph-i-2", "(i) [2]"},
			// Schedule — dedup not extracted (arbitrary slug).
			{"schedule no dedup", "schedule", "FIRST SCHEDULE", "first-schedule", "FIRST SCHEDULE"},
			// Part/chapter — dedup theoretically possible but unlikely.
			{"part no dedup", "part", "Part IV", "part-iv", "Part IV"},
			{"chapter no dedup", "chapter", "Chapter 1", "chapter-1", "Chapter 1"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				sec := makeSection(1, nil, tc.kind, 1, tc.label, "", "", tc.citPath)
				got := sectionCitationPart(&sec)
				if got != tc.want {
					t.Fatalf("sectionCitationPart(kind=%s label=%q citPath=%q) = %q, want %q",
						tc.kind, tc.label, tc.citPath, got, tc.want)
				}
			})
		}
	})

	// Full citation chain: Act 758-style definition section with restarting (a)/(b).
	// Section 2, (1), (a) x 3 — each must produce a unique citation.
	t.Run("definition section unique citations", func(t *testing.T) {
		byID := make(map[int64]*dbsilver.SilverDocumentSection)
		sec := makeSection(1, nil, "section", 2, "Section 2", "Interpretation", "", "section-2")
		byID[1] = &sec
		sub := makeSection(2, sectionID(1), "subsection", 1, "(1)", "", "", "section-2/subsection-1")
		byID[2] = &sub

		// Three (a) paragraphs from three different defined-term blocks.
		paraA1 := makeSection(3, sectionID(2), "paragraph", 1, "(a)", "", "",
			"section-2/subsection-1/paragraph-a")
		paraA2 := makeSection(4, sectionID(2), "paragraph", 2, "(a)", "", "",
			"section-2/subsection-1/paragraph-a-2")
		paraA3 := makeSection(5, sectionID(2), "paragraph", 3, "(a)", "", "",
			"section-2/subsection-1/paragraph-a-3")
		for _, p := range []dbsilver.SilverDocumentSection{paraA1, paraA2, paraA3} {
			byID[p.ID] = &p
		}

		cite1 := sectionCitation(&paraA1, byID)
		cite2 := sectionCitation(&paraA2, byID)
		cite3 := sectionCitation(&paraA3, byID)

		if cite1 == cite2 || cite1 == cite3 || cite2 == cite3 {
			t.Fatalf("citations must be unique: %q, %q, %q", cite1, cite2, cite3)
		}
		// Verify exact format.
		wantCite1 := "Section 2, (1), (a)"
		wantCite2 := "Section 2, (1), (a) [2]"
		wantCite3 := "Section 2, (1), (a) [3]"
		if cite1 != wantCite1 {
			t.Fatalf("cite1 = %q, want %q", cite1, wantCite1)
		}
		if cite2 != wantCite2 {
			t.Fatalf("cite2 = %q, want %q", cite2, wantCite2)
		}
		if cite3 != wantCite3 {
			t.Fatalf("cite3 = %q, want %q", cite3, wantCite3)
		}
	})

	// Verify that a realistic Act 758 definition section with 24 (a) instances
	// produces 24 unique citations.
	t.Run("24-way collision resolved", func(t *testing.T) {
		byID := make(map[int64]*dbsilver.SilverDocumentSection)
		sec := makeSection(1, nil, "section", 2, "Section 2", "", "", "section-2")
		byID[1] = &sec
		sub := makeSection(2, sectionID(1), "subsection", 1, "(1)", "", "", "section-2/subsection-1")
		byID[2] = &sub

		seen := make(map[string]bool)
		for i := 1; i <= 24; i++ {
			var path string
			if i == 1 {
				path = "section-2/subsection-1/paragraph-a"
			} else {
				path = fmt.Sprintf("section-2/subsection-1/paragraph-a-%d", i)
			}
			p := makeSection(int64(2+i), sectionID(2), "paragraph", int32(i), "(a)", "", "", path)
			byID[p.ID] = &p
			cite := sectionCitation(&p, byID)
			if seen[cite] {
				t.Fatalf("duplicate citation at instance %d: %q", i, cite)
			}
			seen[cite] = true
		}
		if len(seen) != 24 {
			t.Fatalf("expected 24 unique citations, got %d", len(seen))
		}
	})
}
