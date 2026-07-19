package etda

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGUIDExtraction(t *testing.T) {
	html := `<div>
<a href="/getattachment/a1b2c3d4-e5f6-7890-abcd-ef1234567890/ETA-Act-2001.pdf.aspx?lang=th-TH">พ.ร.บ. ว่าด้วยธุรกรรมทางอิเล็กทรอนิกส์ พ.ศ. 2544</a>
<a href="https://www.etda.or.th/getattachment/DEADBEEF-1234-5678-9ABC-DEF012345678/Royal-Decree.aspx">พระราชกฤษฎีกา</a>
</div>`
	matches := attachRe.FindAllStringSubmatch(html, -1)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	if got := strings.ToLower(matches[0][1]); got != "a1b2c3d4-e5f6-7890-abcd-ef1234567890" {
		t.Fatalf("guid0 = %q", got)
	}
	if got := matches[0][2]; got != "ETA-Act-2001.pdf.aspx?lang=th-TH" {
		t.Fatalf("filename0 = %q", got)
	}
	if got := cleanTitle(matches[0][3]); got != "พ.ร.บ. ว่าด้วยธุรกรรมทางอิเล็กทรอนิกส์ พ.ศ. 2544" {
		t.Fatalf("title0 = %q", got)
	}
	if got := strings.ToLower(matches[1][1]); got != "deadbeef-1234-5678-9abc-def012345678" {
		t.Fatalf("guid1 = %q", got)
	}
}

func TestDedup(t *testing.T) {
	// Same GUID appears in two listing pages — should be deduplicated.
	page1 := `<a href="/getattachment/aaaa-bbbb-cccc-dddd-eeeeeeeeeeee/doc1.pdf.aspx">Document One</a>`
	page2 := `<a href="/getattachment/aaaa-bbbb-cccc-dddd-eeeeeeeeeeee/doc1.pdf.aspx">Document One</a>
<a href="/getattachment/1111-2222-3333-4444-555555555555/doc2.pdf.aspx">Document Two</a>`

	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
		// Return page1 for first two calls, page2 for third.
		if callCount <= 2 {
			_, _ = w.Write([]byte(page1))
		} else {
			_, _ = w.Write([]byte(page2))
		}
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	// 2 unique GUIDs, not 4 appearances.
	if len(docs) != 2 {
		t.Fatalf("docs = %d, want 2 (dedup by GUID)", len(docs))
	}
	if docs[0].ExternalID != "aaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("doc0 external_id = %q", docs[0].ExternalID)
	}
	if docs[1].ExternalID != "1111-2222-3333-4444-555555555555" {
		t.Errorf("doc1 external_id = %q", docs[1].ExternalID)
	}
}

func TestDiscoverPartialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "DigitalID") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<a href="/getattachment/abcd-1234-5678-9abc-def012345678/test.pdf.aspx">Test Doc</a>`))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err == nil {
		t.Fatal("Discover should return error on partial failure")
	}
	if !strings.Contains(err.Error(), "1 of") {
		t.Fatalf("error should report failure count, got: %v", err)
	}
	if len(docs) == 0 {
		t.Fatal("expected partial docs from successful pages")
	}
}

func TestCleanTitle(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<b>Bold Title</b>", "Bold Title"},
		{"Title &amp; More", "Title & More"},
		{"  spaced   out  ", "spaced out"},
		{"<span class='x'>Nested <b>tags</b></span>", "Nested tags"},
	}
	for _, tt := range tests {
		got := cleanTitle(tt.input)
		if got != tt.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
