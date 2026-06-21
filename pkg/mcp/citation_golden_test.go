package mcp

import "testing"

// Golden regression guard for pathToCitation — the MCP-facing renderer that
// turns a section citation_path ("dieu-1/khoan-2/diem-a") into the human/agent
// citation ("Điều 1, Khoản 2, điểm a"). The multi-jurisdiction seam will move
// these provision labels into config; this test locks the EXACT current output
// so VN citations in the MCP contract stay byte-identical. Note two
// intentionally-preserved quirks: diem renders lowercase "điểm" here (vs
// "Điểm" in gold.chunk.citation), and unknown kinds (incl. "phuluc") pass
// through raw. Do not "fix" these without a deliberate, contract-aware change.
func TestPathToCitationGolden(t *testing.T) {
	cases := []struct{ name, path, want string }{
		{"dieu/khoan/diem", "dieu-1/khoan-2/diem-a", "Điều 1, Khoản 2, điểm a"},
		{"full hierarchy", "phan-1/chuong-I/muc-A/dieu-5", "Phần 1, Chương I, Mục A, Điều 5"},
		{"single dieu", "dieu-16", "Điều 16"},
		{"khoan only", "khoan-3", "Khoản 3"},
		{"diem lowercase d", "diem-a", "điểm a"},
		{"phuluc passes through raw", "phuluc-I", "phuluc-I"},
		{"unknown kind passes through raw", "foo-1", "foo-1"},
		{"segment without dash", "foobar", "foobar"},
		{"mix of known and raw", "dieu-1/foo", "Điều 1, foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathToCitation(tc.path); got != tc.want {
				t.Fatalf("pathToCitation(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
