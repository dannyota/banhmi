package sso

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/ingest"
)

func TestActAnchorParsing(t *testing.T) {
	html := `<div class="browse-list">
<a href="/Act/PSA2019" class="non-ajax browse-title">Payment Services Act 2019</a>
<a href="/Act/CA2018" class="non-ajax browse-title">Cybersecurity Act 2018</a>
<a href="/Act/PDPA2012" class="legislationClass non-ajax">Personal Data Protection Act 2012</a>
</div>`

	matches := actAnchorRe.FindAllStringSubmatch(html, -1)
	if len(matches) != 3 {
		t.Fatalf("anchors = %d, want 3", len(matches))
	}

	tests := []struct {
		wantCode  string
		wantTitle string
	}{
		{"PSA2019", "Payment Services Act 2019"},
		{"CA2018", "Cybersecurity Act 2018"},
		{"PDPA2012", "Personal Data Protection Act 2012"},
	}

	for i, tt := range tests {
		if matches[i][1] != tt.wantCode {
			t.Errorf("match[%d] code = %q, want %q", i, matches[i][1], tt.wantCode)
		}
		if got := cleanTitle(matches[i][2]); got != tt.wantTitle {
			t.Errorf("match[%d] title = %q, want %q", i, got, tt.wantTitle)
		}
	}
}

func TestActAnchorSkipsBoilerplate(t *testing.T) {
	html := `<a href="/Act/BA1970" class="non-ajax">Add to My Collections</a>
<a href="/Act/BA1970?ViewType=Pdf">Download PDF (500 KB)</a>
<a href="/Act/BA1970?ViewType=Rss">Amendments RSS Feed</a>`
	matches := actAnchorRe.FindAllStringSubmatch(html, -1)
	for _, m := range matches {
		title := cleanTitle(m[2])
		if boilerplateTitles[strings.ToLower(title)] || strings.HasPrefix(strings.ToLower(title), "download pdf") {
			continue
		}
		t.Errorf("boilerplate title %q should have been filtered", title)
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Payment Services Act 2019", "Payment Services Act 2019"},
		{"  Cybersecurity   Act  2018  ", "Cybersecurity Act 2018"},
		{"<span>Personal Data</span> Protection Act", "Personal Data Protection Act"},
		{"Banking&amp;Finance Act", "Banking&Finance Act"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := cleanTitle(tt.in); got != tt.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDiscoverDeduplicatesByCode(t *testing.T) {
	// Same Act code on multiple pages should be deduped.
	html := `<div>
<a href="/Act/PSA2019" class="non-ajax browse-title">Payment Services Act 2019</a>
<a href="/Act/PSA2019" class="non-ajax browse-title">Payment Services Act 2019</a>
</div>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	// 26 letters each return the same page, but code is deduped.
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1 (dedup by code)", len(docs))
	}
	if docs[0].ExternalID != "PSA2019" {
		t.Errorf("external_id = %q, want PSA2019", docs[0].ExternalID)
	}
	if docs[0].Number != "PSA2019" {
		t.Errorf("number = %q, want PSA2019", docs[0].Number)
	}
}

func TestDiscoverSetsFileRef(t *testing.T) {
	html := `<a href="/Act/CA2018" class="non-ajax browse-title">Cybersecurity Act 2018</a>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(html))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("no docs discovered")
	}

	doc := docs[0]
	if doc.DetailURL != srv.URL+"/Act/CA2018" {
		t.Errorf("detail_url = %q", doc.DetailURL)
	}
	if len(doc.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(doc.Files))
	}
	f := doc.Files[0]
	if f.URL != srv.URL+"/Act/CA2018?ViewType=Pdf" {
		t.Errorf("file url = %q", f.URL)
	}
	if f.Ext != "pdf" || f.Kind != "main" {
		t.Errorf("file = %+v", f)
	}
	if f.Name != "Cybersecurity Act 2018.pdf" {
		t.Errorf("file name = %q", f.Name)
	}
}

func TestDiscoverContinuesOnPageFailure(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Fail some pages, succeed others.
		if callCount%3 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<a href="/Act/BA1970" class="non-ajax browse-title">Banking Act 1970</a>`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	// Should succeed overall (partial results from successful pages).
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	// Should have found the doc from at least one successful page.
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
}

func TestFetchDetail(t *testing.T) {
	s := New(nil, nil)
	s.baseURL = "https://sso.agc.gov.sg"

	doc, err := s.FetchDetail(context.Background(), ingest.DetailRef{
		ExternalID: "PSA2019",
		DetailURL:  "https://sso.agc.gov.sg/Act/PSA2019",
	})
	if err != nil {
		t.Fatalf("FetchDetail error: %v", err)
	}
	if doc.ExternalID != "PSA2019" {
		t.Errorf("external_id = %q", doc.ExternalID)
	}
	if doc.Number != "PSA2019" {
		t.Errorf("number = %q", doc.Number)
	}
	if string(doc.DocType) != "Act" {
		t.Errorf("doc_type = %q", doc.DocType)
	}
	if len(doc.Files) != 1 {
		t.Fatalf("files = %d", len(doc.Files))
	}
	if doc.Files[0].URL != "https://sso.agc.gov.sg/Act/PSA2019?ViewType=Pdf" {
		t.Errorf("pdf url = %q", doc.Files[0].URL)
	}
}
