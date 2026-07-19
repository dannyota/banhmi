package csa

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseSitemapURLs(t *testing.T) {
	xmlBody := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://www.csa.gov.sg/</loc></url>
  <url><loc>https://www.csa.gov.sg/legislation/codes-of-practice/cybersecurity-code-of-practice</loc></url>
  <url><loc>https://www.csa.gov.sg/legislation/notices/cybersecurity-notice</loc></url>
  <url><loc>https://www.csa.gov.sg/legislation/supplementary-references/some-reference</loc></url>
  <url><loc>https://www.csa.gov.sg/resources/publications/some-pub</loc></url>
  <url><loc>https://www.csa.gov.sg/about-us/contact</loc></url>
  <url><loc>https://www.csa.gov.sg/tips-resources/online-safety</loc></url>
</urlset>`

	urls, err := parseSitemapURLs(xmlBody)
	if err != nil {
		t.Fatalf("parseSitemapURLs error: %v", err)
	}
	if len(urls) != 7 {
		t.Fatalf("urls = %d, want 7", len(urls))
	}
}

func TestIsInScope(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.csa.gov.sg/legislation/codes-of-practice/cybersecurity-code", true},
		{"https://www.csa.gov.sg/legislation/notices/cybersecurity-notice", true},
		{"https://www.csa.gov.sg/legislation/supplementary-references/ref", true},
		{"https://www.csa.gov.sg/resources/publications/some-pub", true},
		{"https://www.csa.gov.sg/about-us/contact", false},
		{"https://www.csa.gov.sg/", false},
		{"https://www.csa.gov.sg/tips-resources/online-safety", false},
		{"https://www.csa.gov.sg/legislation", false}, // exact match, not a prefix
	}
	for _, tt := range tests {
		if got := isInScope(tt.url); got != tt.want {
			t.Errorf("isInScope(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}

func TestURLSlug(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://www.csa.gov.sg/legislation/codes-of-practice/cybersecurity-code", "cybersecurity-code"},
		{"https://www.csa.gov.sg/resources/publications/some-pub/", "some-pub"},
		{"https://www.csa.gov.sg/legislation/notices/notice-123", "notice-123"},
	}
	for _, tt := range tests {
		if got := urlSlug(tt.url); got != tt.want {
			t.Errorf("urlSlug(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestSlugToTitle(t *testing.T) {
	tests := []struct {
		slug string
		want string
	}{
		{"cybersecurity-code-of-practice", "Cybersecurity Code Of Practice"},
		{"some-pub", "Some Pub"},
		{"notice-123", "Notice 123"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := slugToTitle(tt.slug); got != tt.want {
			t.Errorf("slugToTitle(%q) = %q, want %q", tt.slug, got, tt.want)
		}
	}
}

func TestDiscoverFiltersSitemap(t *testing.T) {
	sitemap := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://www.csa.gov.sg/legislation/codes-of-practice/cop-cii</loc></url>
  <url><loc>https://www.csa.gov.sg/legislation/notices/notice-1</loc></url>
  <url><loc>https://www.csa.gov.sg/about-us/contact</loc></url>
  <url><loc>https://www.csa.gov.sg/resources/publications/pub-1</loc></url>
</urlset>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(sitemap))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	// 3 in-scope URLs (codes-of-practice, notices, publications), 1 filtered.
	if len(docs) != 3 {
		t.Fatalf("docs = %d, want 3", len(docs))
	}
	if docs[0].SourceID != "csa" {
		t.Errorf("source_id = %q", docs[0].SourceID)
	}
	if docs[0].ExternalID != "cop-cii" {
		t.Errorf("external_id = %q", docs[0].ExternalID)
	}
}

func TestExtractTitle(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			"h1 preferred over title",
			`<html><head><title>Codes of Practice | CSA Singapore</title></head><body><h1>Codes of Practice for CII</h1></body></html>`,
			"Codes of Practice for CII",
		},
		{
			"title only",
			`<html><head><title>Cybersecurity Notice | CSA Singapore</title></head><body></body></html>`,
			"Cybersecurity Notice",
		},
		{
			"title with dash suffix",
			`<html><head><title>Some Notice - CSA</title></head></html>`,
			"Some Notice",
		},
		{
			"empty",
			`<html><head></head><body></body></html>`,
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractTitle(tt.html); got != tt.want {
				t.Errorf("extractTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractIsomerPDFs(t *testing.T) {
	html := `<div>
		<a href="https://isomer-user-content.by.gov.sg/36/abc-def-123/COP_CII_v2.pdf">Download</a>
		<a href="https://isomer-user-content.by.gov.sg/36/abc-def-123/COP_CII_v2.pdf">Duplicate</a>
		<a href="https://isomer-user-content.by.gov.sg/36/111-222-333/Notice_CII.pdf">Another</a>
	</div>`
	files := extractIsomerPDFs(html)
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2 (deduped)", len(files))
	}
	if files[0].Name != "COP_CII_v2.pdf" {
		t.Errorf("name[0] = %q", files[0].Name)
	}
	if files[0].Ext != "pdf" || files[0].Kind != "main" {
		t.Errorf("file[0] = %+v", files[0])
	}
	if files[1].Name != "Notice_CII.pdf" {
		t.Errorf("name[1] = %q", files[1].Name)
	}
}

func TestExtractIsomerPDFsEmpty(t *testing.T) {
	files := extractIsomerPDFs("<html><body>No PDFs here</body></html>")
	if len(files) != 0 {
		t.Fatalf("files = %d, want 0", len(files))
	}
}
