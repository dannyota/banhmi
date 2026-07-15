package pipeline

import (
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
