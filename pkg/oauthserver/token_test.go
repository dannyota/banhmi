package oauthserver

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"
)

func TestTokenCredentialSeparatesGrantCredentials(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		kind CredentialKind
	}{
		{
			name: "code",
			form: url.Values{"grant_type": {"authorization_code"}, "code": {"code"}, "code_verifier": {"verifier"}, "redirect_uri": {"https://client.example/callback"}},
			kind: CredentialCode,
		},
		{
			name: "refresh",
			form: url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"refresh"}},
			kind: CredentialRefresh,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, _, err := tokenCredential(test.form)
			if err != nil || kind != test.kind {
				t.Fatalf("tokenCredential() = %v, %v", kind, err)
			}
		})
	}
}

func TestTokenCredentialPreservesProtocolErrors(t *testing.T) {
	if _, _, err := tokenCredential(url.Values{"grant_type": {"client_credentials"}}); !errors.Is(err, fosite.ErrUnsupportedGrantType) {
		t.Fatalf("unsupported grant error = %v", err)
	}
	if _, _, err := tokenCredential(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"token"}, "scope": {"other"}}); !errors.Is(err, fosite.ErrInvalidScope) {
		t.Fatalf("refresh scope error = %v", err)
	}
}

func TestNormalizedTokenRequestAddsTrustedAudience(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/token", strings.NewReader("resource="+url.QueryEscape(testResource)))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := request.ParseForm(); err != nil {
		t.Fatal(err)
	}
	normalized := normalizedTokenRequest(request, testResource)
	if normalized.PostForm.Get("audience") != testResource || request.PostForm.Has("audience") {
		t.Fatal("normalization did not add only the trusted audience")
	}
}

func TestTokenCredentialRejectsConflictingCredentials(t *testing.T) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {"code"}, "code_verifier": {"verifier"},
		"redirect_uri": {"https://client.example/callback"}, "refresh_token": {"refresh"},
	}
	if _, _, err := tokenCredential(form); err == nil {
		t.Fatal("tokenCredential() accepted code and refresh credentials")
	}
}

func TestParseOAuthFormRejectsQueryAndDuplicateCredential(t *testing.T) {
	for _, target := range []string{
		"https://evidence.example/oauth/token?client_id=query",
		"https://evidence.example/oauth/token",
	} {
		body := "grant_type=authorization_code&client_id=client&resource=" + url.QueryEscape(testResource) + "&code=one&code=two"
		request := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if _, err := parseOAuthForm(httptest.NewRecorder(), request, tokenFormKeys); err == nil {
			t.Fatalf("parseOAuthForm(%q) error = nil", target)
		}
	}
}
