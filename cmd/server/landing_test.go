package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mountedLanding(t *testing.T, jurisdiction string) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if err := mountLanding(mux, jurisdiction, "v-test", testLogger()); err != nil {
		t.Fatalf("mountLanding(%s): %v", jurisdiction, err)
	}
	return mux
}

func get(t *testing.T, mux *http.ServeMux, path string) (int, string) {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	body, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(body)
}

// TestLandingPerJurisdiction renders both live jurisdictions and checks each
// page carries its own identity — and never the other country's.
func TestLandingPerJurisdiction(t *testing.T) {
	cases := []struct {
		jurisdiction string
		wantStrings  []string
		rejectString string
	}{
		{"vn", []string{"banhmi.danny.vn/mcp", "Điều / Khoản / Điểm", "vbpl.vn", "máy chủ MCP miễn phí", "laksa.danny.vn"}, "Bank Negara"},
		{"my", []string{"laksa.danny.vn/mcp", "Section / Subsection / Paragraph", "bnm.gov.my", "banhmi.danny.vn"}, "Công Báo"},
		{"id", []string{"rendang.danny.vn/mcp", "Pasal / ayat / huruf", "jdih.ojk.go.id", "server MCP gratis", "banhmi.danny.vn", "laksa.danny.vn"}, "Công Báo"},
	}
	for _, c := range cases {
		t.Run(c.jurisdiction, func(t *testing.T) {
			code, body := get(t, mountedLanding(t, c.jurisdiction), "/")
			if code != http.StatusOK {
				t.Fatalf("GET / = %d, want 200", code)
			}
			for _, w := range c.wantStrings {
				if !strings.Contains(body, w) {
					t.Errorf("page missing %q", w)
				}
			}
			if strings.Contains(body, c.rejectString) {
				t.Errorf("page leaked other jurisdiction content %q", c.rejectString)
			}
		})
	}
}

// TestLandingFallsBackToVN guards the compiled-fallback convention: unknown or
// empty jurisdiction must serve VN, never an error page.
func TestLandingFallsBackToVN(t *testing.T) {
	for _, j := range []string{"", "zz"} {
		code, body := get(t, mountedLanding(t, j), "/")
		if code != http.StatusOK || !strings.Contains(body, "banhmi.danny.vn/mcp") {
			t.Errorf("jurisdiction %q: code=%d, want VN fallback page", j, code)
		}
	}
}

// TestLandingStaticSurface covers the SEO/GEO side files and the exact-root
// route (the mux must not swallow /mcp or unknown paths).
func TestLandingStaticSurface(t *testing.T) {
	mux := mountedLanding(t, "vn")

	code, body := get(t, mux, "/robots.txt")
	if code != 200 || !strings.Contains(body, "Sitemap: https://banhmi.danny.vn/sitemap.xml") {
		t.Errorf("robots.txt: code=%d body=%q", code, body)
	}
	code, body = get(t, mux, "/llms.txt")
	if code != 200 || !strings.Contains(body, "https://banhmi.danny.vn/mcp") || !strings.Contains(body, "evidence") {
		t.Errorf("llms.txt: code=%d", code)
	}
	code, body = get(t, mux, "/sitemap.xml")
	if code != 200 || !strings.Contains(body, "<loc>https://banhmi.danny.vn/</loc>") {
		t.Errorf("sitemap.xml: code=%d", code)
	}
	// Exact-root matching: an arbitrary path must 404, not serve the page.
	if code, _ = get(t, mux, "/not-a-page"); code != http.StatusNotFound {
		t.Errorf("GET /not-a-page = %d, want 404", code)
	}
}

// TestLandingListingSurface covers the directory-listing pages (privacy, terms,
// support), the demo-recording redirect, and the ChatGPT apps challenge route —
// the URLs a connector directory submission requires.
func TestLandingListingSurface(t *testing.T) {
	mux := mountedLanding(t, "my")

	code, body := get(t, mux, "/privacy")
	if code != 200 || !strings.Contains(body, "Privacy Policy") || !strings.Contains(body, "no cookies") {
		t.Errorf("/privacy: code=%d", code)
	}
	code, body = get(t, mux, "/terms")
	if code != 200 || !strings.Contains(body, "Not legal advice") {
		t.Errorf("/terms: code=%d", code)
	}
	code, body = get(t, mux, "/support")
	if code != 200 || !strings.Contains(body, "github.com/dannyota/banhmi/issues") {
		t.Errorf("/support: code=%d", code)
	}

	r := httptest.NewRequest("GET", "/demo.mp4", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusFound || !strings.Contains(w.Header().Get("Location"), "laksa-demo.mp4") {
		t.Errorf("/demo.mp4: code=%d location=%q", w.Code, w.Header().Get("Location"))
	}

	code, body = get(t, mux, "/sitemap.xml")
	if code != 200 || !strings.Contains(body, "/privacy</loc>") || !strings.Contains(body, "/support</loc>") {
		t.Errorf("sitemap missing listing pages: code=%d", code)
	}
}

// TestOpenAIAppsChallenge: route mounts only when the env token is set, and the
// per-jurisdiction token wins over the global one.
func TestOpenAIAppsChallenge(t *testing.T) {
	if code, _ := get(t, mountedLanding(t, "vn"), "/.well-known/openai-apps-challenge"); code != http.StatusNotFound {
		t.Errorf("unset token: code=%d, want 404", code)
	}
	t.Setenv("BANHMI_OPENAI_APPS_CHALLENGE", "global-token")
	t.Setenv("BANHMI_OPENAI_APPS_CHALLENGE_VN", "vn-token")
	code, body := get(t, mountedLanding(t, "vn"), "/.well-known/openai-apps-challenge")
	if code != 200 || body != "vn-token" {
		t.Errorf("vn token: code=%d body=%q, want vn-token", code, body)
	}
	code, body = get(t, mountedLanding(t, "my"), "/.well-known/openai-apps-challenge")
	if code != 200 || body != "global-token" {
		t.Errorf("global fallback: code=%d body=%q, want global-token", code, body)
	}
}

// TestLandingJSONLDEscaping guards the FAQ JSON-LD block: quotes in Q/A text
// must stay valid JSON (printf %q in the template).
func TestLandingJSONLDEscaping(t *testing.T) {
	_, body := get(t, mountedLanding(t, "vn"), "/")
	if !strings.Contains(body, `"@type": "FAQPage"`) {
		t.Fatal("FAQPage JSON-LD missing")
	}
	if strings.Contains(body, "&#34;@type&#34;") {
		t.Fatal("JSON-LD got HTML-escaped — script block must render raw JSON")
	}
}
