package congbao

import (
	"strings"
	"testing"
)

func TestExtFromCongbaoURL(t *testing.T) {
	cases := map[string]string{
		// Search API glues the format onto the path with no dot.
		"https://congbaocdn.chinhphu.vn/CongBaoCP/VanBan/2016/12/22071/16437-1-422016tt-btttt16587pdf": "pdf",
		"https://congbaocdn.chinhphu.vn/CongBaoCP/VanBan/2016/1/18986/13509-1-012016tt-btttt13559doc":  "doc",
		"https://x/y/file.docx": "docx",
		"https://x/y/FILE.PDF":  "pdf",
		"https://x/y/no-ext":    "",
	}
	for url, want := range cases {
		if got := extFromCongbaoURL(url); got != want {
			t.Errorf("extFromCongbaoURL(%q) = %q, want %q", url, got, want)
		}
	}
}

// TestFileRefsFromSearchIgnoresGarbageExt feeds the real shape the search API
// returns: file_extension is a path tail and the URL has the format glued on with
// no dot. The ref must still come out as a usable .pdf with the right MIME.
func TestFileRefsFromSearchIgnoresGarbageExt(t *testing.T) {
	files := []searchFile{{
		URL:   "https://congbaocdn.chinhphu.vn/CongBaoCP/VanBan/2016/12/22071/16437-1-422016tt-btttt16587pdf",
		Order: 1,
		Name:  "42_2016_TT-BTTTT(16587)",
		Ext:   "vn/CongBaoCP/VanBan/2016/12/22071/16437-1-422016tt-btttt16587pdf", // garbage path, must be ignored
	}}
	refs := fileRefsFromSearch(files)
	if len(refs) != 1 {
		t.Fatalf("got %d refs, want 1", len(refs))
	}
	r := refs[0]
	if r.Ext != "pdf" {
		t.Errorf("Ext = %q, want pdf", r.Ext)
	}
	if r.MIMEType != "application/pdf" {
		t.Errorf("MIMEType = %q, want application/pdf", r.MIMEType)
	}
	if !strings.HasSuffix(r.Name, ".pdf") {
		t.Errorf("Name = %q, want a .pdf suffix", r.Name)
	}
}
