package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"danny.vn/banhmi/pkg/fetch"
	banhmimcp "danny.vn/banhmi/pkg/mcp"
)

func TestBrowserShowsEvidenceWarningsAndSafeDocumentUI(t *testing.T) {
	if os.Getenv("BANHMI_BROWSER_TESTS") != "1" {
		t.Skip("set BANHMI_BROWSER_TESTS=1 in the hosted browser job")
	}
	chromePath, err := exec.LookPath("google-chrome")
	if err != nil {
		t.Fatalf("BANHMI_BROWSER_TESTS=1 requires google-chrome: %v", err)
	}

	evidence := browserEvidence(t)
	mux := http.NewServeMux()
	testServer := httptest.NewUnstartedServer(mux)
	testServer.StartTLS()
	t.Cleanup(testServer.Close)

	origin, err := url.Parse(testServer.URL)
	if err != nil {
		t.Fatalf("parse test origin: %v", err)
	}
	server, err := New(evidence, authenticated(), origin)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server.Register(mux)

	baseContext, cancelBase := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelBase()
	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.NoSandbox,
		chromedp.Flag("headless", true),
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.Flag("disable-gpu", true),
	)
	allocatorContext, cancelAllocator := chromedp.NewExecAllocator(baseContext, allocatorOptions...)
	defer cancelAllocator()
	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	defer cancelBrowser()

	var visibleWarnings string
	var injectedElementCount int
	var unsafeLinkCount int
	var injectedScriptRan bool
	var openDetailsCount int
	var documentText string
	err = chromedp.Do(browserContext,
		chromedp.Navigate(testServer.URL+"/app"),
		chromedp.WaitVisible(chromedp.CSS("#search-form")),
		chromedp.SendKeys(chromedp.CSS("#query"), "bằng chứng tổng hợp"),
		chromedp.Click(chromedp.CSS(`#search-form button[type="submit"]`)),
		chromedp.WaitVisible(chromedp.CSS("#results .result-card")),
		chromedp.WaitVisible(chromedp.CSS("#results .evidence-alerts")),
		browserEvaluate(`document.querySelector("#results").innerText`, &visibleWarnings),
		browserEvaluate(`document.querySelectorAll("#injected-script").length`, &injectedElementCount),
		browserEvaluate(`document.querySelectorAll('a[href^="javascript:"]').length`, &unsafeLinkCount),
		browserEvaluate(`Boolean(globalThis.injected)`, &injectedScriptRan),
		browserEvaluate(`document.querySelectorAll("#results details[open]").length`, &openDetailsCount),
		chromedp.Click(chromedp.CSS("#results .result-card .secondary-button")),
		chromedp.WaitVisible(chromedp.CSS("#document-panel")),
		chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
			var err error
			documentText, err = chromedp.Text(chromedp.CSS("#document-panel"))(ctx, target)
			return err
		}),
	)
	if err != nil {
		t.Fatalf("run hosted browser check, Chrome must be installed when BANHMI_BROWSER_TESTS=1: %v", err)
	}
	for _, want := range []string{"Evidence is insufficient", "Blocking gap", "Non-binding text", "Text needs review", "1 confirmed relation"} {
		if !strings.Contains(visibleWarnings, want) {
			t.Errorf("visible results missing %q: %q", want, visibleWarnings)
		}
	}
	if injectedElementCount != 0 || unsafeLinkCount != 0 || injectedScriptRan {
		t.Errorf("remote evidence created injected elements, ran script, or made unsafe links: injected=%d script_ran=%t unsafe_links=%d", injectedElementCount, injectedScriptRan, unsafeLinkCount)
	}
	if openDetailsCount != 0 {
		t.Errorf("visible-warning proof opened expandable evidence details: open=%d", openDetailsCount)
	}
	for _, want := range []string{"Document evidence", "Synthetic document provision"} {
		if !strings.Contains(documentText, want) {
			t.Errorf("document panel missing %q: %q", want, documentText)
		}
	}
}

// browserEvaluate preserves the previous zero value for JavaScript null.
func browserEvaluate[T any](expression string, destination *T) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		value, err := chromedp.Evaluate[T](expression)(ctx, target)
		if errors.Is(err, chromedp.ErrJSNull) {
			err = nil
		}
		if err == nil {
			*destination = value
		}
		return err
	})
}

func TestBrowserCookieMintersPreserveSession(t *testing.T) {
	if os.Getenv("BANHMI_BROWSER_TESTS") != "1" {
		t.Skip("set BANHMI_BROWSER_TESTS=1 in the hosted browser job")
	}
	chromePath, err := exec.LookPath("google-chrome")
	if err != nil {
		t.Fatalf("BANHMI_BROWSER_TESTS=1 requires google-chrome: %v", err)
	}
	t.Setenv("BANHMI_CHROME_PATH", chromePath)
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	for _, name := range []string{"cloudflare", "aws-waf", "ojk", "null-user-agent", "undefined-user-agent"} {
		t.Run(name, func(t *testing.T) {
			seenUA := make(chan string, 1)
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				select {
				case seenUA <- r.UserAgent():
				default:
				}
				for _, cookieName := range []string{"cf_clearance", "aws-waf-token", "c_ojk", "app_session"} {
					http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "synthetic-cookie-value", Path: "/"})
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				switch name {
				case "null-user-agent":
					_, _ = w.Write([]byte(`<script>Object.defineProperty(navigator, "userAgent", {value: null})</script>`))
				case "undefined-user-agent":
					_, _ = w.Write([]byte(`<script>Object.defineProperty(navigator, "userAgent", {value: undefined})</script>`))
				default:
					_, _ = w.Write([]byte(`<p>Synthetic challenge completed</p>`))
				}
			}))
			defer fixture.Close()
			var output bytes.Buffer
			log := slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var mint func(context.Context) (string, string, error)
			switch name {
			case "aws-waf":
				mint = (&fetch.AWSWAFMinter{ChallengeURL: fixture.URL, Timeout: 15 * time.Second, Log: log}).Mint
			case "ojk":
				mint = (&fetch.OJKMinter{ChallengeURL: fixture.URL, Timeout: 15 * time.Second, Log: log}).Mint
			default:
				mint = (&fetch.CloudflareMinter{ChallengeURL: fixture.URL, Timeout: 15 * time.Second, Log: log}).Mint
			}
			header, ua, err := mint(ctx)
			if name == "undefined-user-agent" {
				if !errors.Is(err, chromedp.ErrJSUndefined) || header != "" || ua != "" {
					t.Fatal("undefined user agent must preserve the wrapped error and empty results")
				}
				return
			}
			if err != nil {
				t.Fatalf("mint synthetic browser session: %v", err)
			}
			wantUA := <-seenUA
			if name == "null-user-agent" {
				wantUA = ""
			}
			if ua != wantUA {
				t.Fatal("minted user agent differs from browser user agent")
			}
			parts := strings.Split(header, "; ")
			if len(parts) != 4 {
				t.Fatal("cookie header must retain every browser cookie")
			}
			cookies := make(map[string]bool)
			for _, part := range parts {
				cookies[part] = true
			}
			for _, cookieName := range []string{"cf_clearance", "aws-waf-token", "c_ojk", "app_session"} {
				if !cookies[cookieName+"=synthetic-cookie-value"] {
					t.Fatal("cookie header changed a cookie name or value")
				}
			}
			if strings.Contains(output.String(), "synthetic-cookie-value") {
				t.Fatal("browser cookie value leaked into logs")
			}
		})
	}
}

func browserEvidence(t *testing.T) *fakeEvidence {
	t.Helper()
	var search banhmimcp.SearchOutput
	decodeBrowserFixture(t, `{
		"hits":[{
			"doc_number":"01/2026/TT-TEST",
			"title":"Synthetic evidence <script id=\"injected-script\">globalThis.injected=true</script>",
			"source_url":"javascript:globalThis.injected=true",
			"location":"Điều 1",
			"snippet":"Synthetic non-binding evidence",
			"document_id":7,
			"chunk_id":9,
			"validity":{"status_class":"in_force","status_label":"In force"},
			"text_provenance":{"has_binding_text":true,"has_nonbinding_text":true,"needs_review":true,"quality":"synthetic review fixture"},
			"relations":[{"relation_id":3,"direction":"incoming","relation_type":"amends_supplements","resolved":true,"target_indexed":true,"target_has_binding_text":true}]
		}],
		"gaps":[{"kind":"known_binding_text_gap","message":"Synthetic binding text is incomplete","blocks_answer":true}],
		"abstain":true,
		"scope":{"checked":true,"in_domain":true,"known_reference":false}
	}`, &search)
	var document banhmimcp.DocumentOutput
	decodeBrowserFixture(t, `{
		"found":true,
		"document":{"document_id":7,"doc_number":"01/2026/TT-TEST","title":"Synthetic document","validity":{"status_class":"in_force"}},
		"text_summary":{"has_binding_text":true,"has_nonbinding_text":false,"needs_review":false},
		"chunks":[{"chunk_id":9,"location":"Điều 1","content":"Synthetic document provision","ordinal":1,"validity":{"status_class":"in_force"},"text_provenance":{"has_binding_text":true,"has_nonbinding_text":false,"needs_review":false}}],
		"limit":20,
		"offset":0
	}`, &document)
	return &fakeEvidence{searchOut: search, documentOut: document}
}

func decodeBrowserFixture(t *testing.T, raw string, destination any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), destination); err != nil {
		t.Fatalf("decode browser fixture: %v", err)
	}
}
