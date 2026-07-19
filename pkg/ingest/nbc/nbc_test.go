package nbc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPDFSlug(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://www.nbc.gov.kh/download_files/legislation/prakas_eng/Prakas-on-Banking.pdf", "Prakas-on-Banking"},
		{"/download_files/legislation/law/Banking-Law-2008.pdf", "Banking-Law-2008"},
	}
	for _, tt := range tests {
		if got := pdfSlug(tt.url); got != tt.want {
			t.Errorf("pdfSlug(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestSlugToTitle(t *testing.T) {
	tests := []struct {
		slug string
		want string
	}{
		{"Prakas-on-Banking", "Prakas On Banking"},
		{"Banking_Law_2008", "Banking Law 2008"},
	}
	for _, tt := range tests {
		if got := slugToTitle(tt.slug); got != tt.want {
			t.Errorf("slugToTitle(%q) = %q, want %q", tt.slug, got, tt.want)
		}
	}
}

func TestAnchorTitle(t *testing.T) {
	tests := []struct {
		html string
		want string
	}{
		// Plain text.
		{"Prakas on Banking", "Prakas on Banking"},
		// Trailing date in <span>.
		{"PRAKAS ON CAPITAL BUFFER, <span style='color:#FF0000'><i>January 9, 2026</i></span>", "PRAKAS ON CAPITAL BUFFER"},
		// Trailing comma-date without HTML.
		{"Prakas on Licensing, March 30, 2021", "Prakas on Licensing"},
		// No date, just trailing comma.
		{"Prakas on Licensing,", "Prakas on Licensing"},
		// Empty.
		{"", ""},
		// Date only.
		{"<span><i>January 9, 2026</i></span>", ""},
	}
	for _, tt := range tests {
		if got := anchorTitle(tt.html); got != tt.want {
			t.Errorf("anchorTitle(%q) = %q, want %q", tt.html, got, tt.want)
		}
	}
}

func TestDiscoverParsesPages(t *testing.T) {
	const page = `<html><body>
<a href="/download_files/legislation/prakas_eng/Prakas-on-Banking.pdf">Prakas on Banking</a>
<a href="/download_files/legislation/prakas_eng/Prakas-on-Payments.pdf">Prakas on Payments</a>
<a href="/download_files/legislation/prakas_eng/Prakas-on-Banking.pdf">Duplicate</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	s := New(nil, srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	// 3 pages x same response, but dedup removes duplicates.
	// Each page contributes 2 unique PDFs on first pass, then 0 on subsequent.
	if len(docs) != 2 {
		t.Fatalf("docs = %d, want 2 (deduped across pages)", len(docs))
	}
	if docs[0].ExternalID != "Prakas-on-Banking" {
		t.Errorf("ExternalID = %q", docs[0].ExternalID)
	}
	if docs[0].Title != "Prakas on Banking" {
		t.Errorf("Title = %q, want %q", docs[0].Title, "Prakas on Banking")
	}
	if len(docs[0].Files) != 1 {
		t.Fatalf("Files = %d, want 1", len(docs[0].Files))
	}
}

func TestDiscoverSingleQuotedAnchors(t *testing.T) {
	// Realistic NBC HTML with single-quoted hrefs, spans, and dates.
	const page = `<html><body>
<a href='../../download_files/legislation/prakas_eng/47.pdf' target='_blank'>PRAKAS on Third-Party Processors, <span style='color:#FF0000'><i>September 22, 2010</i></span></a>
<a href='../../download_files/legislation/prakas_eng/1380B7-011-001.pdf' target='_blank'>Circular on the Implementation of Prakas on the Calculation of Banks' Net Worth, <span style='color:#FF0000'><i>February 23, 2011</i></span></a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	s := New(nil, srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(docs) < 2 {
		t.Fatalf("docs = %d, want >= 2", len(docs))
	}
	// First doc: numbered PDF with anchor-text title.
	if docs[0].ExternalID != "47" {
		t.Errorf("ExternalID = %q, want 47", docs[0].ExternalID)
	}
	if docs[0].Title != "PRAKAS on Third-Party Processors" {
		t.Errorf("Title = %q, want %q", docs[0].Title, "PRAKAS on Third-Party Processors")
	}
	// Second doc: code-named PDF.
	if docs[1].Title != "Circular on the Implementation of Prakas on the Calculation of Banks' Net Worth" {
		t.Errorf("Title = %q", docs[1].Title)
	}
}

func TestSourceID(t *testing.T) {
	s := New(nil, nil, nil)
	if s.ID() != "nbc" {
		t.Fatalf("ID() = %q, want nbc", s.ID())
	}
}
