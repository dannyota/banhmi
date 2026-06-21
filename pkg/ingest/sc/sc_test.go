package sc

import (
	"strings"
	"testing"
)

func TestDocAnchorParsing(t *testing.T) {
	html := `<ul>
<li><a href="https://www.sc.com.my/api/documentms/download.ashx?id=2f253636-07dd-4355-b89e-010b2ef581c1">Guidelines on Technology Risk Management (pdf)</a></li>
<li><a href="/api/documentms/download.ashx?id=985D39B2-D548-4E57-AE55-B141159FD20A">Summary of Amendments&nbsp;(PDF)</a></li>
</ul>`
	matches := docAnchorRe.FindAllStringSubmatch(html, -1)
	if len(matches) != 2 {
		t.Fatalf("anchors = %d, want 2", len(matches))
	}
	if strings.ToLower(matches[0][1]) != "2f253636-07dd-4355-b89e-010b2ef581c1" {
		t.Fatalf("guid0 = %q", matches[0][1])
	}
	if got := cleanTitle(matches[0][2]); got != "Guidelines on Technology Risk Management" {
		t.Fatalf("title0 = %q", got)
	}
	if got := cleanTitle(matches[1][2]); got != "Summary of Amendments" {
		t.Fatalf("title1 = %q (nbsp/(PDF) not stripped)", got)
	}
}

func TestFileFor(t *testing.T) {
	f := fileFor("https://www.sc.com.my", "abc-123", "Guidelines on Cyber Risk")
	if f.URL != "https://www.sc.com.my/api/documentms/download.ashx?id=abc-123" {
		t.Fatalf("url = %q", f.URL)
	}
	if f.Ext != "pdf" || f.Kind != "main" || f.Name != "Guidelines on Cyber Risk.pdf" {
		t.Fatalf("file = %+v", f)
	}
}
