package serc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseBoardEntries(t *testing.T) {
	html := `<table>
<tr><td><a href="/boards/data_dir/m23prakas/Prakas-on-Governance-2020.pdf">Prakas on Governance 2020</a></td></tr>
<tr><td><a href="/boards/data_dir/m23prakas/Prakas-on-Disclosure-2019.pdf">Prakas on Disclosure 2019</a></td></tr>
<tr><td><a href="/boards/data_dir/m23prakas/Prakas-on-Governance-2020.pdf">Duplicate</a></td></tr>
</table>`

	board := boardSection{ID: "m23prakas", DocType: "Prakas"}
	docs := parseBoardEntries(html, board, "https://serc.gov.kh")
	// parseBoardEntries does not dedup — that happens in discoverBoard.
	if len(docs) != 3 {
		t.Fatalf("docs = %d, want 3 (before dedup)", len(docs))
	}
	if docs[0].ExternalID != "m23prakas/Prakas-on-Governance-2020" {
		t.Errorf("ExternalID = %q", docs[0].ExternalID)
	}
	if docs[0].Files[0].URL != "https://serc.gov.kh/boards/data_dir/m23prakas/Prakas-on-Governance-2020.pdf" {
		t.Errorf("File URL = %q", docs[0].Files[0].URL)
	}
	if string(docs[0].DocType) != "Prakas" {
		t.Errorf("DocType = %q", docs[0].DocType)
	}
}

func TestHasNextPage(t *testing.T) {
	html := `<a href="?bid=m23prakas&nav=list&p=1">1</a> <a href="?bid=m23prakas&nav=list&p=2">2</a> <a href="?bid=m23prakas&nav=list&p=3">3</a>`
	if !hasNextPage(html, 1) {
		t.Error("expected next page for page 1")
	}
	if !hasNextPage(html, 2) {
		t.Error("expected next page for page 2")
	}
	if hasNextPage(html, 3) {
		t.Error("expected no next page for page 3")
	}
}

func TestSlugToTitle(t *testing.T) {
	tests := []struct {
		slug string
		want string
	}{
		{"Prakas-on-Governance-2020", "Prakas On Governance 2020"},
		{"Law_on_Securities", "Law On Securities"},
	}
	for _, tt := range tests {
		if got := slugToTitle(tt.slug); got != tt.want {
			t.Errorf("slugToTitle(%q) = %q, want %q", tt.slug, got, tt.want)
		}
	}
}

func TestDiscoverWithHTTPTest(t *testing.T) {
	const listing = `<html><body>
<a href="/boards/data_dir/m21laws/Securities-Law.pdf">Securities Law</a>
<a href="?bid=m21laws&nav=list&p=1">1</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(listing))
	}))
	defer srv.Close()

	s := New(srv.Client(), nil)
	s.baseURL = srv.URL

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover error: %v", err)
	}
	// 5 boards × 1 entry each from the same response, but the entry's boardID is m21laws
	// so only boards whose listing has matching data_dir entries produce docs.
	if len(docs) == 0 {
		t.Fatal("expected at least one doc")
	}
}

func TestSourceID(t *testing.T) {
	s := New(nil, nil)
	if s.ID() != "serc" {
		t.Fatalf("ID() = %q, want serc", s.ID())
	}
}
