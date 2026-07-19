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

func TestTitleSlug(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"Guidelines on Recognized Markets", "GUIDELINES-ON-RECOGNIZED-MARKETS"},
		{"Guidelines on Digital Assets", "GUIDELINES-ON-DIGITAL-ASSETS"},
		{"Guidelines on Technology Risk Management", "GUIDELINES-ON-TECHNOLOGY-RISK-MANAGEMENT"},
		{"Summary of Amendments to the Guidelines on Digital Assets", "SUMMARY-OF-AMENDMENTS-TO-THE-GUIDELINES-ON-DIGITAL-ASSETS"},
		{"Response Paper 1/2016 – Regulatory Framework for Cyber Security Resilience",
			"RESPONSE-PAPER-1-2016-REGULATORY-FRAMEWORK-FOR-CYBER-SECURITY-RESILIENCE"},
		{"Capital Market and Services (Prescription of Securities) (Digital Currency and Digital Token) Order 2019",
			"CAPITAL-MARKET-AND-SERVICES-PRESCRIPTION-OF-SECURITIES-DIGITAL-CURRENCY-AND-DIGITAL-TOKEN-ORDER-2019"},
		{"Frequently-Asked Questions on Digital Asset Exchange (DAX) Framework",
			"FREQUENTLY-ASKED-QUESTIONS-ON-DIGITAL-ASSET-EXCHANGE-DAX-FRAMEWORK"},
		{"Appendix 2 (Application for Registration as an IEO Operator)",
			"APPENDIX-2-APPLICATION-FOR-REGISTRATION-AS-AN-IEO-OPERATOR"},
		{"Guidelines on Prevention of Money Laundering & Terrorism Financing for Capital Market Intermediaries",
			"GUIDELINES-ON-PREVENTION-OF-MONEY-LAUNDERING-TERRORISM-FINANCING-FOR-CAPITAL-MARKET-INTERMEDIARIES"},
		// Edge: already clean
		{"SIMPLE", "SIMPLE"},
		// Edge: empty after strip
		{"", ""},
	}
	for _, tt := range tests {
		got := titleSlug(tt.title)
		if got != tt.want {
			t.Errorf("titleSlug(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}

func TestScDocNumber(t *testing.T) {
	tests := []struct {
		slug string
		want string
	}{
		{"GUIDELINES-ON-RECOGNIZED-MARKETS", "SC-GL/GUIDELINES-ON-RECOGNIZED-MARKETS"},
		{"GUIDELINES-ON-DIGITAL-ASSETS", "SC-GL/GUIDELINES-ON-DIGITAL-ASSETS"},
	}
	for _, tt := range tests {
		got := scDocNumber(tt.slug)
		if got != tt.want {
			t.Errorf("scDocNumber(%q) = %q, want %q", tt.slug, got, tt.want)
		}
	}
}

func TestDiscoverDeduplicatesByTitle(t *testing.T) {
	// Simulate a section page with the same document linked 3 times under
	// different GUIDs (SC's real pattern: one GUID per part/chapter link).
	html := `<ul>
<li><a href="/api/documentms/download.ashx?id=aaaa-1111-bbbb">Guidelines on Recognized Markets (pdf)</a></li>
<li><a href="/api/documentms/download.ashx?id=cccc-2222-dddd">Guidelines on Recognized Markets (pdf)</a></li>
<li><a href="/api/documentms/download.ashx?id=eeee-3333-ffff">Guidelines on Recognized Markets (pdf)</a></li>
<li><a href="/api/documentms/download.ashx?id=1111-aaaa-2222">Guidelines on Digital Assets (pdf)</a></li>
</ul>`
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
	// Only 2 unique documents, not 4 GUIDs.
	if len(docs) != 2 {
		t.Fatalf("docs = %d, want 2 (dedup by title)", len(docs))
	}
	// First seen GUID wins.
	if docs[0].ExternalID != "aaaa-1111-bbbb" {
		t.Errorf("first doc external_id = %q, want aaaa-1111-bbbb", docs[0].ExternalID)
	}
	// Number is set.
	if docs[0].Number != "SC-GL/GUIDELINES-ON-RECOGNIZED-MARKETS" {
		t.Errorf("first doc number = %q, want SC-GL/GUIDELINES-ON-RECOGNIZED-MARKETS", docs[0].Number)
	}
	if docs[1].Number != "SC-GL/GUIDELINES-ON-DIGITAL-ASSETS" {
		t.Errorf("second doc number = %q, want SC-GL/GUIDELINES-ON-DIGITAL-ASSETS", docs[1].Number)
	}
}

func TestDiscoverSetsNumberForAllDocs(t *testing.T) {
	// Each section returns a unique document — verify all get stable numbers.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<a href="/api/documentms/download.ashx?id=dead-beef-0001">Guidelines on Technology Risk Management (pdf)</a>`))
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
	// The same title across sections is also deduped — only one doc.
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1 (same title across sections deduped)", len(docs))
	}
	if docs[0].Number != "SC-GL/GUIDELINES-ON-TECHNOLOGY-RISK-MANAGEMENT" {
		t.Errorf("number = %q", docs[0].Number)
	}
}
