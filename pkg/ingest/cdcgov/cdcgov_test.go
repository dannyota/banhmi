package cdcgov

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
		{"https://cdc.gov.kh/wp-content/uploads/2020/07/Law-on-Banking.pdf", "Law-on-Banking"},
		{"https://cdc.gov.kh/wp-content/uploads/2021/01/Financial-Leasing.pdf", "Financial-Leasing"},
		{"https://cdc.gov.kh/wp-content/uploads/2019/03/Prakas_NBC.PDF", "Prakas_NBC"},
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
		{"Law-on-Banking", "Law On Banking"},
		{"Financial_Leasing", "Financial Leasing"},
		{"Prakas_NBC", "Prakas NBC"},
	}
	for _, tt := range tests {
		if got := slugToTitle(tt.slug); got != tt.want {
			t.Errorf("slugToTitle(%q) = %q, want %q", tt.slug, got, tt.want)
		}
	}
}

func TestDiscoverParsesPage(t *testing.T) {
	const page = `<html><body>
<h2>Financial Laws</h2>
<a href="https://cdc.gov.kh/wp-content/uploads/2020/07/Law-on-Banking.pdf">Law on Banking</a>
<a href="https://cdc.gov.kh/wp-content/uploads/2021/01/Insurance-Law.pdf">Insurance Law</a>
<a href="https://cdc.gov.kh/wp-content/uploads/2020/07/Law-on-Banking.pdf">Duplicate</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %d, want 2 (deduped)", len(docs))
	}
	if docs[0].ExternalID != "Law-on-Banking" {
		t.Errorf("docs[0].ExternalID = %q", docs[0].ExternalID)
	}
	if len(docs[0].Files) != 1 {
		t.Fatalf("docs[0].Files = %d, want 1", len(docs[0].Files))
	}
	if docs[0].Files[0].Ext != "pdf" {
		t.Errorf("file ext = %q, want pdf", docs[0].Files[0].Ext)
	}
}

func TestSourceID(t *testing.T) {
	s := New(nil, nil)
	if s.ID() != "cdc" {
		t.Fatalf("ID() = %q, want cdc", s.ID())
	}
}
