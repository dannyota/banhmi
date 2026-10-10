package oauthserver

import (
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ory/fosite"
)

func TestPendingAuthorizationRoundTrip(t *testing.T) {
	challenge := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	values := url.Values{
		"client_id":             {"approved-client"},
		"redirect_uri":          {"https://client.example/callback"},
		"response_type":         {"code"},
		"scope":                 {"mcp:read"},
		"resource":              {testResource},
		"state":                 {"opaque-client-state"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	payload, err := encodePendingAuthorization(values)
	if err != nil {
		t.Fatalf("encodePendingAuthorization() error = %v", err)
	}
	request, err := decodePendingAuthorization(payload)
	if err != nil {
		t.Fatalf("decodePendingAuthorization() error = %v", err)
	}
	if got := request.URL.Query().Get("state"); got != "opaque-client-state" {
		t.Fatalf("state = %q", got)
	}
}

func TestPendingAuthorizationRejectsUnknownAndDuplicateFields(t *testing.T) {
	challenge := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	base := "https://evidence.example/oauth/authorize?client_id=approved-client&redirect_uri=https%3A%2F%2Fclient.example%2Fcallback&response_type=code&scope=mcp%3Aread&resource=" + url.QueryEscape(testResource) + "&code_challenge=" + challenge + "&code_challenge_method=S256"
	for _, suffix := range []string{"&audience=unexpected", "&client_id=second"} {
		r := httptest.NewRequest("GET", base+suffix, nil)
		if _, err := validateAuthorizeParameters(r.URL.Query(), testResource); err == nil {
			t.Fatalf("validateAuthorizeParameters(%q) error = nil", suffix)
		}
	}
}

func TestPendingAuthorizationRequiresCanonicalS256Challenge(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	base := url.Values{
		"client_id": {"approved-client"}, "redirect_uri": {"https://client.example/callback"},
		"response_type": {"code"}, "scope": {"mcp:read"}, "resource": {testResource},
		"code_challenge": {valid}, "code_challenge_method": {"S256"},
	}
	tests := map[string]func(url.Values){
		"missing": func(values url.Values) { values.Del("code_challenge") },
		"plain": func(values url.Values) { values.Set("code_challenge_method", "plain") },
		"padded": func(values url.Values) { values.Set("code_challenge", valid+"=") },
		"wrong alphabet": func(values url.Values) { values.Set("code_challenge", "+"+valid[1:]) },
		"wrong length": func(values url.Values) { values.Set("code_challenge", base64.RawURLEncoding.EncodeToString(make([]byte, 31))) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			values := cloneForm(base)
			mutate(values)
			if _, err := validateAuthorizeParameters(values, testResource); !errors.Is(err, fosite.ErrInvalidRequest) {
				t.Fatalf("error = %v, want invalid_request", err)
			}
		})
	}
}

func TestAuthorizeParameterErrorsPreserveProtocolType(t *testing.T) {
	valid := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	base := url.Values{
		"client_id": {"approved-client"}, "redirect_uri": {"https://client.example/callback"},
		"response_type": {"code"}, "scope": {"mcp:read"}, "resource": {testResource},
		"code_challenge": {valid}, "code_challenge_method": {"S256"},
	}
	tests := []struct {
		name string
		key string
		value string
		want error
	}{
		{name: "scope", key: "scope", value: "other", want: fosite.ErrInvalidScope},
		{name: "response type", key: "response_type", value: "token", want: fosite.ErrUnsupportedResponseType},
		{name: "resource", key: "resource", value: "https://other.example/mcp", want: errInvalidTarget},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := cloneForm(base)
			values.Set(test.key, test.value)
			if _, err := validateAuthorizeParameters(values, testResource); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPendingAuthorizationRejectsMalformedRedirectPort(t *testing.T) {
	registry, err := ParseClientRegistry([]byte(`{
		"version":1,
		"clients":[{
			"client_id":"approved-client",
			"client_name":"Approved client",
			"redirect_uris":["http://127.0.0.1:49152/callback"],
			"token_endpoint_auth_method":"none",
			"grant_types":["authorization_code","refresh_token"],
			"response_types":["code"],
			"scope":"mcp:read"
		}]
	}`), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	values := url.Values{
		"client_id":             {"approved-client"},
		"redirect_uri":          {"http://127.0.0.1:29999/callback"},
		"response_type":         {"code"},
		"scope":                 {"mcp:read"},
		"resource":              {testResource},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(make([]byte, 32))},
		"code_challenge_method": {"S256"},
	}
	if err := registry.ValidateRedirect(values.Get("client_id"), values.Get("redirect_uri")); err == nil {
		t.Fatal("ValidateRedirect() accepted an unregistered loopback port")
	}
}
