package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/identity"
	banhmimcp "danny.vn/banhmi/pkg/mcp"
	"danny.vn/banhmi/pkg/rag/retrieve"
)

const testOrigin = "https://banhmi.example"

type fakeAuth struct {
	principal identity.Principal
	err       error
	csrf      string
	validCSRF bool
}

func (f *fakeAuth) Authenticate(*http.Request) (identity.Principal, error) {
	if f.err != nil {
		return identity.Principal{}, f.err
	}
	return f.principal, nil
}

func (f *fakeAuth) CSRFToken(*http.Request) string { return f.csrf }

func (f *fakeAuth) ValidCSRF(_ *http.Request, token string) bool {
	return f.validCSRF && token == f.csrf
}

type fakeEvidence struct {
	searchOut   banhmimcp.SearchOutput
	documentOut banhmimcp.DocumentOutput
	err         error
	searchCalls int
	docCalls    int
	searchIn    banhmimcp.SearchInput
	documentIn  banhmimcp.DocumentInput
	deadline    bool
}

func (f *fakeEvidence) Search(ctx context.Context, in banhmimcp.SearchInput) (banhmimcp.SearchOutput, error) {
	f.searchCalls++
	f.searchIn = in
	_, f.deadline = ctx.Deadline()
	return f.searchOut, f.err
}

func (f *fakeEvidence) Document(ctx context.Context, in banhmimcp.DocumentInput) (banhmimcp.DocumentOutput, error) {
	f.docCalls++
	f.documentIn = in
	_, f.deadline = ctx.Deadline()
	return f.documentOut, f.err
}

func TestNewValidatesDependenciesAndCanonicalOrigin(t *testing.T) {
	evidence := &fakeEvidence{}
	auth := authenticated()
	cases := []struct {
		name     string
		evidence Evidence
		auth     Auth
		origin   string
	}{
		{name: "missing evidence", auth: auth, origin: testOrigin},
		{name: "missing auth", evidence: evidence, origin: testOrigin},
		{name: "non HTTPS origin", evidence: evidence, auth: auth, origin: "http://banhmi.example"},
		{name: "origin with path", evidence: evidence, auth: auth, origin: testOrigin + "/app"},
		{name: "origin with credentials", evidence: evidence, auth: auth, origin: "https://user@banhmi.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			origin, err := url.Parse(tc.origin)
			if err != nil {
				t.Fatalf("parse origin: %v", err)
			}
			if _, err := New(tc.evidence, tc.auth, origin); err == nil {
				t.Fatal("New error = nil")
			}
		})
	}
}

func TestPublicAndAccountPages(t *testing.T) {
	t.Run("landing is indexable and self contained", func(t *testing.T) {
		rr := serve(t, &fakeEvidence{}, authenticated(), http.MethodGet, "/", nil, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		body := rr.Body.String()
		for _, want := range []string{"Vietnamese regulatory evidence", "Try evidence", "Connect MCP", "/privacy", "/terms"} {
			if !strings.Contains(body, want) {
				t.Errorf("landing missing %q", want)
			}
		}
		if got := rr.Header().Get("X-Robots-Tag"); got != "" {
			t.Errorf("X-Robots-Tag = %q, want empty", got)
		}
		assertSecurityHeaders(t, rr)
	})

	t.Run("login accepts only public destinations", func(t *testing.T) {
		for _, tc := range []struct{ target, want string }{
			{target: "/app", want: "/auth/google/start?next=%2Fapp"},
			{target: "/connect", want: "/auth/google/start?next=%2Fconnect"},
			{target: "https://attacker.example", want: "/auth/google/start?next=%2Fapp"},
			{target: "//attacker.example", want: "/auth/google/start?next=%2Fapp"},
		} {
			rr := serve(t, &fakeEvidence{}, &fakeAuth{err: identity.ErrUnauthenticated}, http.MethodGet, "/login?next="+url.QueryEscape(tc.target), nil, nil)
			if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), tc.want) {
				t.Errorf("next %q: status %d body %q, want action %q", tc.target, rr.Code, rr.Body.String(), tc.want)
			}
			assertPrivateHeaders(t, rr)
		}
	})

	t.Run("account pages redirect without a session", func(t *testing.T) {
		for _, path := range []string{"/app", "/connect"} {
			rr := serve(t, &fakeEvidence{}, &fakeAuth{err: identity.ErrUnauthenticated}, http.MethodGet, path, nil, nil)
			if rr.Code != http.StatusSeeOther {
				t.Errorf("%s status = %d, want 303", path, rr.Code)
			}
			if got, want := rr.Header().Get("Location"), "/login?next="+url.QueryEscape(path); got != want {
				t.Errorf("%s Location = %q, want %q", path, got, want)
			}
		}
	})

	t.Run("application carries session CSRF without exposing markup", func(t *testing.T) {
		auth := authenticated()
		auth.principal.DisplayName = `<img src=x onerror=alert(1)>`
		rr := serve(t, &fakeEvidence{}, auth, http.MethodGet, "/app", nil, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		body := rr.Body.String()
		for _, want := range []string{"Tìm kiếm văn bản, điều khoản hoặc nghĩa vụ", `name="csrf_token"`, `content="csrf-value"`, "Evidence only", "Vietnamese query"} {
			if !strings.Contains(body, want) {
				t.Errorf("app missing %q", want)
			}
		}
		if strings.Contains(body, `<img src=x`) {
			t.Error("display name rendered as markup")
		}
		assertPrivateHeaders(t, rr)
	})

	t.Run("connect uses the configured canonical resource", func(t *testing.T) {
		rr := serve(t, &fakeEvidence{}, authenticated(), http.MethodGet, "/connect", nil, nil)
		body := rr.Body.String()
		for _, want := range []string{"https://banhmi.example/mcp", "OAuth", "Google account"} {
			if !strings.Contains(body, want) {
				t.Errorf("connect missing %q", want)
			}
		}
		if strings.Contains(strings.ToLower(body), "api key") {
			t.Error("connect page mentions API keys")
		}
		assertPrivateHeaders(t, rr)
	})
}

func TestAuthenticationFailuresSeparateDenialFromServiceOutage(t *testing.T) {
	t.Run("account page redirects denied sessions", func(t *testing.T) {
		for _, authErr := range []error{identity.ErrUnauthenticated, identity.ErrBlocked} {
			rr := serve(t, &fakeEvidence{}, &fakeAuth{err: authErr}, http.MethodGet, "/app", nil, nil)
			if rr.Code != http.StatusSeeOther {
				t.Errorf("error %v: status = %d, want 303", authErr, rr.Code)
			}
		}
	})

	t.Run("account page reports authentication storage outage", func(t *testing.T) {
		rr := serve(t, &fakeEvidence{}, &fakeAuth{err: errors.New("database secret-detail")}, http.MethodGet, "/app", nil, nil)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rr.Code)
		}
		if rr.Header().Get("Location") != "" || strings.Contains(rr.Body.String(), "secret-detail") {
			t.Errorf("outage response redirected or disclosed the error: headers=%v body=%q", rr.Header(), rr.Body.String())
		}
		assertPrivateHeaders(t, rr)
	})

	t.Run("login reports authentication storage outage", func(t *testing.T) {
		rr := serve(t, &fakeEvidence{}, &fakeAuth{err: errors.New("database secret-detail")}, http.MethodGet, "/login?next=/app", nil, nil)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rr.Code)
		}
		if strings.Contains(rr.Body.String(), "secret-detail") || strings.Contains(rr.Body.String(), "Continue with Google") {
			t.Errorf("login outage response obscured or disclosed the failure: %q", rr.Body.String())
		}
	})

	t.Run("API distinguishes denied session from outage", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			authErr    error
			wantStatus int
		}{
			{name: "unauthenticated", authErr: identity.ErrUnauthenticated, wantStatus: http.StatusUnauthorized},
			{name: "blocked", authErr: identity.ErrBlocked, wantStatus: http.StatusUnauthorized},
			{name: "storage outage", authErr: errors.New("database secret-detail"), wantStatus: http.StatusServiceUnavailable},
		} {
			t.Run(tc.name, func(t *testing.T) {
				evidence := &fakeEvidence{}
				rr := serve(t, evidence, &fakeAuth{err: tc.authErr}, http.MethodPost, "/api/v1/search", []byte(`{"query":"an toàn"}`), apiHeaders(testOrigin, "csrf-value"))
				if rr.Code != tc.wantStatus {
					t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
				}
				if evidence.searchCalls != 0 || strings.Contains(rr.Body.String(), "secret-detail") {
					t.Errorf("API invoked evidence or disclosed auth failure: calls=%d body=%q", evidence.searchCalls, rr.Body.String())
				}
			})
		}
	})
}

func TestEvidenceAPIDeniesRequestsBeforeEvidenceRuns(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		path       string
		headers    map[string]string
		body       []byte
		auth       *fakeAuth
		wantStatus int
	}{
		{name: "method", method: http.MethodGet, path: "/api/v1/search", auth: authenticated(), wantStatus: http.StatusMethodNotAllowed},
		{name: "session", method: http.MethodPost, path: "/api/v1/search", auth: &fakeAuth{err: identity.ErrUnauthenticated}, headers: map[string]string{"Authorization": "Bearer cannot-substitute"}, wantStatus: http.StatusUnauthorized},
		{name: "origin", method: http.MethodPost, path: "/api/v1/search", auth: authenticated(), headers: apiHeaders("https://other.example", "csrf-value"), body: []byte(`{"query":"an toàn"}`), wantStatus: http.StatusForbidden},
		{name: "csrf", method: http.MethodPost, path: "/api/v1/search", auth: authenticated(), headers: apiHeaders(testOrigin, "wrong"), body: []byte(`{"query":"an toàn"}`), wantStatus: http.StatusForbidden},
		{name: "malformed JSON", method: http.MethodPost, path: "/api/v1/search", auth: authenticated(), headers: apiHeaders(testOrigin, "csrf-value"), body: []byte(`{"query":`), wantStatus: http.StatusBadRequest},
		{name: "empty search object", method: http.MethodPost, path: "/api/v1/search", auth: authenticated(), headers: apiHeaders(testOrigin, "csrf-value"), body: []byte(`{}`), wantStatus: http.StatusBadRequest},
		{name: "null search", method: http.MethodPost, path: "/api/v1/search", auth: authenticated(), headers: apiHeaders(testOrigin, "csrf-value"), body: []byte(`null`), wantStatus: http.StatusBadRequest},
		{name: "whitespace search", method: http.MethodPost, path: "/api/v1/search", auth: authenticated(), headers: apiHeaders(testOrigin, "csrf-value"), body: []byte(`{"query":"  "}`), wantStatus: http.StatusBadRequest},
		{name: "empty document object", method: http.MethodPost, path: "/api/v1/document", auth: authenticated(), headers: apiHeaders(testOrigin, "csrf-value"), body: []byte(`{}`), wantStatus: http.StatusBadRequest},
		{name: "null document", method: http.MethodPost, path: "/api/v1/document", auth: authenticated(), headers: apiHeaders(testOrigin, "csrf-value"), body: []byte(`null`), wantStatus: http.StatusBadRequest},
		{name: "blank document number", method: http.MethodPost, path: "/api/v1/document", auth: authenticated(), headers: apiHeaders(testOrigin, "csrf-value"), body: []byte(`{"doc_number":"  "}`), wantStatus: http.StatusBadRequest},
		{name: "unknown JSON field", method: http.MethodPost, path: "/api/v1/document", auth: authenticated(), headers: apiHeaders(testOrigin, "csrf-value"), body: []byte(`{"document_id":4,"unknown":true}`), wantStatus: http.StatusBadRequest},
		{name: "oversized body", method: http.MethodPost, path: "/api/v1/document", auth: authenticated(), headers: apiHeaders(testOrigin, "csrf-value"), body: bytes.Repeat([]byte("x"), maxRequestBody+1), wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			evidence := &fakeEvidence{}
			rr := serve(t, evidence, tc.auth, tc.method, tc.path, tc.body, tc.headers)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%q", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if evidence.searchCalls != 0 || evidence.docCalls != 0 {
				t.Errorf("evidence calls = search %d document %d, want zero", evidence.searchCalls, evidence.docCalls)
			}
			assertPrivateHeaders(t, rr)
		})
	}
}

func TestSearchAPIUsesDirectMCPPayloadAndDefaults(t *testing.T) {
	searcher := &operationSearcher{}
	operations := banhmimcp.New(searcher, nil)
	direct, err := operations.Search(context.Background(), banhmimcp.SearchInput{Query: "an toàn thông tin"})
	if err != nil {
		t.Fatalf("direct Search: %v", err)
	}
	rr := serve(t, operations, authenticated(), http.MethodPost, "/api/v1/search", []byte(`{"query":"an toàn thông tin"}`), apiHeaders(testOrigin, "csrf-value"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}
	assertJSONEqual(t, rr.Body.Bytes(), direct)
	if body := rr.Body.String(); !strings.Contains(body, `"warning"`) || !strings.Contains(body, `"has_nonbinding_text":true`) || !strings.Contains(body, `"relations"`) || !strings.Contains(body, `"gaps"`) {
		t.Errorf("search response dropped warning, non-binding text, relation, or gap evidence: %s", body)
	}
	if searcher.lastOpts.TopK != 0 || searcher.lastOpts.InForceOnly != nil {
		t.Errorf("search defaults changed: %+v", searcher.lastOpts)
	}
	assertPrivateHeaders(t, rr)
}

func TestDocumentAPIPreservesNotFoundPayloadAndAddsDeadline(t *testing.T) {
	var output banhmimcp.DocumentOutput
	if err := json.Unmarshal([]byte(`{"found":false,"text_summary":{"has_binding_text":false,"has_nonbinding_text":false,"needs_review":false},"chunks":[],"gaps":[{"kind":"no_evidence","message":"document not found","blocks_answer":true}],"limit":20,"offset":0}`), &output); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	evidence := &fakeEvidence{documentOut: output}
	rr := serve(t, evidence, authenticated(), http.MethodPost, "/api/v1/document", []byte(`{"document_id":42,"citation":"Điều 7"}`), apiHeaders(testOrigin, "csrf-value"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", rr.Code, rr.Body.String())
	}
	assertJSONEqual(t, rr.Body.Bytes(), output)
	if evidence.documentIn.DocumentID != 42 || evidence.documentIn.Citation != "Điều 7" {
		t.Errorf("document input = %+v", evidence.documentIn)
	}
	if !evidence.deadline {
		t.Error("evidence context has no deadline")
	}
}

func TestEvidenceFailureIsGeneric(t *testing.T) {
	evidence := &fakeEvidence{err: errors.New("database password secret-detail")}
	rr := serve(t, evidence, authenticated(), http.MethodPost, "/api/v1/search", []byte(`{"query":"an toàn"}`), apiHeaders(testOrigin, "csrf-value"))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret-detail") {
		t.Errorf("response disclosed dependency error: %q", rr.Body.String())
	}
}

func TestAppJavaScriptUsesSafeDOMAndHTTPLinks(t *testing.T) {
	rr := serve(t, &fakeEvidence{}, authenticated(), http.MethodGet, assetJSPath, nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := rr.Body.String()
	for _, forbidden := range []string{"innerHTML", "insertAdjacentHTML", "document.write"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("JavaScript contains unsafe sink %q", forbidden)
		}
	}
	for _, want := range []string{"textContent", `protocol === "http:"`, `protocol === "https:"`, "noopener", "noreferrer"} {
		if !strings.Contains(body, want) {
			t.Errorf("JavaScript missing %q", want)
		}
	}
}

type operationSearcher struct {
	lastOpts retrieve.SearchOpts
}

func (s *operationSearcher) Search(_ context.Context, _ string, opts retrieve.SearchOpts) ([]retrieve.Hit, error) {
	s.lastOpts = opts
	return []retrieve.Hit{{
		ChunkID:    8,
		DocumentID: 4,
		DocNumber:  "09/2026/TT-NHNN",
		Title:      "Quy định an toàn thông tin",
		IssuedDate: "2026-02-01",
		Citation:   "Điều 7, Khoản 2",
		Content:    "Tổ chức tín dụng phải bảo vệ hệ thống thông tin.",
		Validity: retrieve.ValidityEvidence{
			StatusClass:   "in_force",
			EffectiveFrom: "2026-01-01",
		},
		Text: retrieve.TextEvidence{
			HasBindingText:    true,
			HasNonBindingText: true,
			NeedsReview:       true,
			Authorities:       []string{"official_text", "translation"},
		},
		Relations: []retrieve.Relation{{
			RelationID:   2,
			RelationType: "amends_supplements",
			DocNumber:    "01/2024/TT-NHNN",
			Resolved:     true,
		}},
	}}, nil
}

func (s *operationSearcher) SearchEvidence(ctx context.Context, query string, opts retrieve.SearchOpts) (retrieve.Evidence, error) {
	hits, err := s.Search(ctx, query, opts)
	return retrieve.Evidence{
		Hits: hits,
		Gaps: []retrieve.Gap{{Kind: retrieve.GapKnownBindingTextGap, DocNumber: "09/2026/TT-NHNN"}},
		Scope: retrieve.ScopeEvidence{Checked: true, InDomain: true},
	}, err
}

func authenticated() *fakeAuth {
	return &fakeAuth{
		principal: identity.Principal{AccountID: 7, Email: "reader@example.test", DisplayName: "Reader"},
		csrf:      "csrf-value",
		validCSRF: true,
	}
}

func serve(t *testing.T, evidence Evidence, auth Auth, method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	origin, err := url.Parse(testOrigin)
	if err != nil {
		t.Fatalf("parse origin: %v", err)
	}
	server, err := New(evidence, auth, origin)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	server.Register(mux)
	req := httptest.NewRequest(method, testOrigin+target, bytes.NewReader(body))
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func apiHeaders(origin, csrf string) map[string]string {
	return map[string]string{
		"Content-Type": "application/json",
		"Origin":       origin,
		"X-CSRF-Token": csrf,
	}
}

func assertJSONEqual(t *testing.T, got []byte, want any) {
	t.Helper()
	var gotValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal expected: %v", err)
	}
	var wantValue any
	if err := json.Unmarshal(wantBytes, &wantValue); err != nil {
		t.Fatalf("decode expected: %v", err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("JSON response differs:\ngot:  %#v\nwant: %#v", gotValue, wantValue)
	}
}

func assertSecurityHeaders(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if got := rr.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") || !strings.Contains(got, "script-src 'self'") {
		t.Errorf("Content-Security-Policy = %q", got)
	}
	for name, want := range map[string]string{
		"Referrer-Policy":       "no-referrer",
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
	} {
		if got := rr.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func assertPrivateHeaders(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	assertSecurityHeaders(t, rr)
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rr.Header().Get("X-Robots-Tag"); got != "noindex, nofollow" {
		t.Errorf("X-Robots-Tag = %q, want noindex, nofollow", got)
	}
}
