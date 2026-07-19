package odc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPackageToDoc(t *testing.T) {
	pkg := ckanPackage{
		ID:            "test-pkg-id",
		Name:          "banking-law-2008",
		Title:         "Law on Banking and Financial Institutions",
		Notes:         "Regulates banking and financial institutions in Cambodia.",
		DocNumber:     json.RawMessage(`"NS/RKM/1108/024"`),
		DocType:       json.RawMessage(`"Law"`),
		EffectiveDate: "2008-11-18",
		LawsStatus:    json.RawMessage(`"In force"`),
		Resources: []ckanResource{
			{ID: "res-1", URL: "https://example.com/banking-law.pdf", Name: "banking-law.pdf", Format: "PDF"},
			{ID: "res-2", URL: "https://example.com/data.csv", Name: "data.csv", Format: "CSV"},
		},
	}
	doc := packageToDoc(pkg)
	if doc.SourceID != "odc" {
		t.Errorf("SourceID = %q", doc.SourceID)
	}
	if doc.ExternalID != "test-pkg-id" {
		t.Errorf("ExternalID = %q", doc.ExternalID)
	}
	if doc.Number != "NS/RKM/1108/024" {
		t.Errorf("Number = %q", doc.Number)
	}
	if doc.Title != "Law on Banking and Financial Institutions" {
		t.Errorf("Title = %q", doc.Title)
	}
	if doc.Status != "In force" {
		t.Errorf("Status = %q", doc.Status)
	}
	wantDate := time.Date(2008, 11, 18, 0, 0, 0, 0, time.UTC)
	if !doc.EffectiveAt.Equal(wantDate) {
		t.Errorf("EffectiveAt = %v, want %v", doc.EffectiveAt, wantDate)
	}
	// Only PDF resources should be included (CSV is filtered).
	if len(doc.Files) != 1 {
		t.Fatalf("Files = %d, want 1 (CSV filtered)", len(doc.Files))
	}
	if doc.Files[0].Ext != "pdf" {
		t.Errorf("File ext = %q", doc.Files[0].Ext)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"short", 10, "short"},
		{"hello world this is long", 11, "hello"},
		{"nospaces", 4, "nosp"},
	}
	for _, tt := range tests {
		if got := truncate(tt.input, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
		}
	}
}

func TestDiscoverCKAN(t *testing.T) {
	const resp = `{
		"success": true,
		"result": {
			"count": 1,
			"results": [{
				"id": "pkg-1",
				"name": "banking-law",
				"title": "Banking Law",
				"notes": "A law about banking.",
				"resources": [
					{"id": "r1", "url": "https://example.com/banking.pdf", "name": "banking.pdf", "format": "PDF"}
				]
			}]
		}
	}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(resp))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	// Multiple search queries all hit the same mock, but dedup by ID.
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1 (deduped)", len(docs))
	}
	if docs[0].ExternalID != "pkg-1" {
		t.Errorf("ExternalID = %q", docs[0].ExternalID)
	}
}

func TestSourceID(t *testing.T) {
	s := New(nil, nil)
	if s.ID() != "odc" {
		t.Fatalf("ID() = %q, want odc", s.ID())
	}
}
