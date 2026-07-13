package sc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestDiscoverPartialSectionFailureReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fail one section, succeed the others.
		if strings.HasSuffix(r.URL.Path, "/digital-assets") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Return a page with one document link for successful sections.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<ul><li><a href="/api/documentms/download.ashx?id=aaaa-bbbb-cccc">Tech Risk Guide (pdf)</a></li></ul>`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err == nil {
		t.Fatal("Discover should return non-nil error when a section fails")
	}
	if !strings.Contains(err.Error(), "1 of") {
		t.Fatalf("error should report failure count, got: %v", err)
	}
	// Partial docs from successful sections are still returned.
	if len(docs) == 0 {
		t.Fatal("expected partial docs from successful sections")
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
