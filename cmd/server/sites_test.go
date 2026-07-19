package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"danny.vn/banhmi/pkg/base/config"
)

// stubSite makes a site whose handler just reports which jurisdiction served the
// request, so routing can be asserted without a database.
func stubSite(code, domain string) *site {
	return &site{
		code:   code,
		domain: domain,
		handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, code)
		}),
	}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// TestRouterServesTheRequestedCorpus is the load-bearing test of the
// single-server design: serving one country's question from another country's
// corpus would cite the wrong law. That must never happen silently.
func TestRouterServesTheRequestedCorpus(t *testing.T) {
	sites := []*site{
		stubSite("vn", "banhmi.danny.vn"),
		stubSite("my", "laksa.danny.vn"),
		stubSite("id", "rendang.danny.vn"),
	}
	r := router(sites, discard())

	tests := []struct {
		name       string
		header     string
		host       string
		wantBody   string
		wantStatus int
	}{
		{"header wins (CloudFront injects it)", "id", "banhmi.danny.vn", "id", http.StatusOK},
		{"header vn", "vn", "", "vn", http.StatusOK},
		{"header my", "my", "", "my", http.StatusOK},
		{"host fallback when no header", "", "laksa.danny.vn", "my", http.StatusOK},
		{"host fallback with port", "", "rendang.danny.vn:8080", "id", http.StatusOK},
		{"unknown host falls back to default", "", "example.com", "vn", http.StatusOK},
		// An explicit but unknown jurisdiction must 404 — NOT quietly fall back to
		// another country's corpus.
		{"unknown jurisdiction 404s, never falls back", "sg", "banhmi.danny.vn", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			if tt.header != "" {
				req.Header.Set(jurisdictionHeader, tt.header)
			}
			if tt.host != "" {
				req.Host = tt.host
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusOK && w.Body.String() != tt.wantBody {
				t.Fatalf("served by %q, want %q — WRONG CORPUS", w.Body.String(), tt.wantBody)
			}
		})
	}
}

// TestRecoverPanicContainsOneJurisdiction: with all jurisdictions in one process,
// an unhandled panic would otherwise take down VN, MY and ID together.
func TestRecoverPanicContainsOneJurisdiction(t *testing.T) {
	boom := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	h := recoverPanic(boom, discard())

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (panic must be contained, not crash the process)", w.Code)
	}
}

func TestJurisdictionCodes(t *testing.T) {
	tests := []struct {
		env      string
		fallback string
		want     []string
	}{
		{"", "vn", []string{"vn"}},                      // unset: single-jurisdiction deploys unchanged
		{"vn,my,id", "vn", []string{"vn", "my", "id"}},  // the prod set
		{" VN , my ,, my ", "vn", []string{"vn", "my"}}, // trims, lowercases, dedupes
		{",,", "my", []string{"my"}},                    // junk falls back
	}
	for _, tt := range tests {
		t.Setenv("BANHMI_JURISDICTIONS", tt.env)
		if tt.env == "" {
			_ = os.Unsetenv("BANHMI_JURISDICTIONS")
		}
		got := jurisdictionCodes(tt.fallback)
		if len(got) != len(tt.want) {
			t.Fatalf("codes(%q) = %v, want %v", tt.env, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Fatalf("codes(%q) = %v, want %v", tt.env, got, tt.want)
			}
		}
	}
}

// TestSiteConfigNeverSharesADatabase: one corpus per country is the product's
// core invariant — a shared pool would let one country's answer cite another's law.
func TestSiteConfigNeverSharesADatabase(t *testing.T) {
	base := loadTestConfig(t)
	seen := map[string]string{}
	for _, code := range []string{"vn", "my", "id"} {
		cfg := siteConfig(base, code)
		if cfg.Jurisdiction != code {
			t.Fatalf("siteConfig(%q).Jurisdiction = %q", code, cfg.Jurisdiction)
		}
		if prev, dup := seen[cfg.Database.DBName]; dup {
			t.Fatalf("%q and %q would share database %q", prev, code, cfg.Database.DBName)
		}
		seen[cfg.Database.DBName] = code
	}
}

func loadTestConfig(t *testing.T) *config.Config {
	t.Helper()
	c := config.Default()
	c.Jurisdiction = "vn"
	return c
}

// TestSiteConfigKeepsPerJurisdictionRetrievalTuning: one process serves all
// countries, so a single BANHMI_HNSW_CANDIDATE_MULTIPLIER env var cannot express
// three values. VN must stay on the exact scan (a golden case ranks >1200 deep,
// so any ANN candidate pool misses it) while MY/ID keep HNSW. Getting this wrong
// silently degrades VN recall in production.
func TestSiteConfigKeepsPerJurisdictionRetrievalTuning(t *testing.T) {
	base := loadTestConfig(t)
	base.Retrieve.HNSWCandidateMultiplier = 24 // the config default (HNSW)

	if got := siteConfig(base, "vn").Retrieve.HNSWCandidateMultiplier; got != -1 {
		t.Fatalf("vn multiplier = %d, want -1 (exact scan) — VN on ANN loses a golden case", got)
	}
	for _, code := range []string{"my", "id"} {
		if got := siteConfig(base, code).Retrieve.HNSWCandidateMultiplier; got != 24 {
			t.Fatalf("%s multiplier = %d, want 24 (HNSW) — exact scan would be slow", code, got)
		}
	}

	// Per-jurisdiction env still overrides, for operator tuning without a redeploy.
	t.Setenv("BANHMI_HNSW_CANDIDATE_MULTIPLIER_ID", "48")
	if got := siteConfig(base, "id").Retrieve.HNSWCandidateMultiplier; got != 48 {
		t.Fatalf("id env override = %d, want 48", got)
	}
	if got := siteConfig(base, "vn").Retrieve.HNSWCandidateMultiplier; got != -1 {
		t.Fatalf("vn must not pick up ID's override, got %d", got)
	}
}
