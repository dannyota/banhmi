package oauthserver

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"

	"danny.vn/banhmi/pkg/identity"
)

func TestAuthorizeDoesNotRedirectUnregisteredLoopbackPort(t *testing.T) {
	registry, err := ParseClientRegistry([]byte(`{
		"version":1,
		"clients":[{
			"client_id":"native",
			"client_name":"Native client",
			"redirect_uris":["http://127.0.0.1:49152/callback"],
			"token_endpoint_auth_method":"none",
			"grant_types":["authorization_code","refresh_token"],
			"response_types":["code"],
			"scope":"mcp:read"
		}]
	}`), testResource)
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{clients: registry, cfg: Config{Issuer: "https://evidence.example", Resource: testResource}}
	values := url.Values{
		"client_id": {"native"}, "redirect_uri": {"http://127.0.0.1:29999/callback"},
		"response_type": {"code"}, "scope": {"mcp:read"}, "resource": {testResource},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(make([]byte, 32))}, "code_challenge_method": {"S256"},
	}
	recorder := httptest.NewRecorder()
	server.handleAuthorize(recorder, httptest.NewRequest(http.MethodGet, "https://evidence.example/oauth/authorize?"+values.Encode(), nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if location := recorder.Header().Get("Location"); location != "" {
		t.Fatalf("Location = %q", location)
	}
}

func TestAuthorizeRejectsOversizeAndInvalidUTF8BeforeDependencies(t *testing.T) {
	server := &Server{cfg: Config{Issuer: "https://evidence.example", Resource: testResource}}
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "https://evidence.example/oauth/authorize?"+strings.Repeat("a", maxAuthorizeQuerySize+1), nil),
		httptest.NewRequest(http.MethodGet, "https://evidence.example/oauth/authorize", nil),
		httptest.NewRequest(http.MethodGet, "https://evidence.example/oauth/authorize", nil),
	}
	requests[1].URL.RawQuery = string([]byte{'a', '=', 0xff})
	requests[2].URL.RawQuery = "state=%FF"
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		server.handleAuthorize(recorder, request)
		if recorder.Code != http.StatusBadRequest || recorder.Header().Get("Location") != "" {
			t.Fatalf("status = %d, Location = %q", recorder.Code, recorder.Header().Get("Location"))
		}
	}
}

func TestAuthorizeErrorRedirectIncludesIssuerAndState(t *testing.T) {
	server := &Server{cfg: Config{Issuer: "https://evidence.example"}}
	recorder := httptest.NewRecorder()
	server.writeRawAuthorizeErrorRedirect(recorder, "https://client.example/callback", "opaque-state", fosite.ErrAccessDenied)
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("iss") != "https://evidence.example" || location.Query().Get("state") != "opaque-state" || location.Query().Get("error") != "access_denied" {
		t.Fatalf("redirect query = %v", location.Query())
	}
}

func TestPendingConsentHandleHashesDecodedCookie(t *testing.T) {
	raw := make([]byte, 32)
	request := httptest.NewRequest(http.MethodGet, "https://evidence.example/oauth/resume", nil)
	request.AddCookie(&http.Cookie{Name: identity.PendingConsentCookieName, Value: base64.RawURLEncoding.EncodeToString(raw)})
	decoded, gotHash, err := pendingConsentHandle(request)
	if err != nil {
		t.Fatalf("pendingConsentHandle() error = %v", err)
	}
	if string(decoded) != string(raw) || string(gotHash) != string(hashOAuthHandle(raw)) {
		t.Fatal("pendingConsentHandle() did not hash the decoded cookie")
	}
}

func TestNormalizedAuthorizeRequestTreatsExplicitQueryAsDefault(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://evidence.example/oauth/authorize?response_mode=query&state=opaque", nil)
	normalized := normalizedAuthorizeRequest(request, request.URL.Query(), testResource)
	if normalized.URL.Query().Has("response_mode") {
		t.Fatal("normalized request retained explicit query response mode")
	}
	if normalized.URL.Query().Get("state") != "opaque" || request.URL.Query().Get("response_mode") != "query" {
		t.Fatal("normalization changed another field or the original request")
	}
	if normalized.URL.Query().Get("audience") != testResource || request.URL.Query().Has("audience") {
		t.Fatal("normalization did not add only the trusted audience")
	}
}
