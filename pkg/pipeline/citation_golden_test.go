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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("sectionCitation = %q, want %q", tc.got, tc.want)
			}
		})
	}
}
