package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

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
	err = chromedp.Run(browserContext,
		chromedp.Navigate(testServer.URL+"/app"),
		chromedp.WaitVisible("#search-form", chromedp.ByQuery),
		chromedp.SendKeys("#query", "bằng chứng tổng hợp", chromedp.ByQuery),
		chromedp.Click(`#search-form button[type="submit"]`, chromedp.ByQuery),
		chromedp.WaitVisible("#results .result-card", chromedp.ByQuery),
		chromedp.WaitVisible("#results .evidence-alerts", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector("#results").innerText`, &visibleWarnings),
		chromedp.Evaluate(`document.querySelectorAll("#injected-script").length`, &injectedElementCount),
		chromedp.Evaluate(`document.querySelectorAll('a[href^="javascript:"]').length`, &unsafeLinkCount),
		chromedp.Evaluate(`Boolean(globalThis.injected)`, &injectedScriptRan),
		chromedp.Evaluate(`document.querySelectorAll("#results details[open]").length`, &openDetailsCount),
		chromedp.Click("#results .result-card .secondary-button", chromedp.ByQuery),
		chromedp.WaitVisible("#document-panel", chromedp.ByQuery),
		chromedp.Text("#document-panel", &documentText, chromedp.ByQuery),
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
