package pdpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestListingResponseParsing(t *testing.T) {
	const raw = `{
		"totalItems": 2,
		"data": [
			{"id": "aaa-bbb-ccc", "topic": "Advisory Guidelines", "title": "Advisory Guidelines on the PDPA for Selected Topics", "href": "/organisations/advisory-guidelines-selected-topics", "date": "23 Sep 2013"},
			{"id": "ddd-eee-fff", "topic": "Enforcement Decisions", "title": "Some Enforcement Decision", "href": "/organisations/enforcement", "date": "01 Jan 2020"}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(raw))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	// Only "Advisory Guidelines" is in scope; "Enforcement Decisions" is filtered.
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1 (filtered by topic)", len(docs))
	}
	d := docs[0]
	if d.ExternalID != "aaa-bbb-ccc" {
		t.Errorf("external_id = %q", d.ExternalID)
	}
	if d.Title != "Advisory Guidelines on the PDPA for Selected Topics" {
		t.Errorf("title = %q", d.Title)
	}
	if string(d.DocType) != "Advisory Guidelines" {
		t.Errorf("doc_type = %q", d.DocType)
	}
	wantDate := time.Date(2013, 9, 23, 0, 0, 0, 0, time.UTC)
	if !d.IssuedAt.Equal(wantDate) {
		t.Errorf("issued_at = %v, want %v", d.IssuedAt, wantDate)
	}
}

func TestListingResponseWithSectorSpecific(t *testing.T) {
	const raw = `{
		"totalItems": 3,
		"data": [
			{"id": "111", "topic": "Advisory Guidelines", "title": "AG Title", "href": "/ag", "date": "01 Jan 2020"},
			{"id": "222", "topic": "Sector-Specific Guidelines", "title": "SS Title", "href": "/ss", "date": "15 Mar 2021"},
			{"id": "333", "topic": "Other Topic", "title": "Other Title", "href": "/other", "date": "20 Jun 2022"}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(raw))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("docs = %d, want 2 (Advisory + Sector-Specific)", len(docs))
	}
}

func TestExtractPDFs(t *testing.T) {
	html := `<div>
		<a href="/assets/12345678-1234-1234-1234-123456789abc">Download PDF</a>
		<a href="/assets/5bfe2a77-00a0-4836-94bb-5cf432c8f92d">Icon</a>
		<a href="/assets/ee462a25-5953-4484-8cf0-88bde43d21bc">Another Icon</a>
		<a href="/assets/abcdefab-cdef-abcd-efab-cdefabcdefab">Another PDF</a>
		<a href="/assets/12345678-1234-1234-1234-123456789abc">Duplicate</a>
	</div>`
	files := extractPDFs(html)
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2 (icons filtered, dups removed)", len(files))
	}
	if files[0].URL != "https://files.app.optical.gov.sg/pdpc/production/assets/12345678-1234-1234-1234-123456789abc.pdf" {
		t.Errorf("url[0] = %q", files[0].URL)
	}
	if files[0].Ext != "pdf" || files[0].Kind != "main" {
		t.Errorf("file[0] = %+v", files[0])
	}
	if files[1].URL != "https://files.app.optical.gov.sg/pdpc/production/assets/abcdefab-cdef-abcd-efab-cdefabcdefab.pdf" {
		t.Errorf("url[1] = %q", files[1].URL)
	}
}

func TestExtractPDFsEmpty(t *testing.T) {
	files := extractPDFs("<html><body>No assets here</body></html>")
	if len(files) != 0 {
		t.Fatalf("files = %d, want 0", len(files))
	}
}

func TestDateParsing(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"23 Sep 2013", time.Date(2013, 9, 23, 0, 0, 0, 0, time.UTC)},
		{"01 Jan 2020", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"15 Mar 2021", time.Date(2021, 3, 15, 0, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		got, err := time.Parse("02 Jan 2006", tt.input)
		if err != nil {
			t.Errorf("Parse(%q) error: %v", tt.input, err)
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("Parse(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
