package oauthserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ory/fosite"
)

func TestProtectRejectsNonHeaderCredentialsBeforeHandler(t *testing.T) {
	server := &Server{cfg: Config{Resource: testResource}}
	called := false
	handler := server.Protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	requests := []*http.Request{
		httptest.NewRequest(http.MethodPost, "https://evidence.example/mcp?access_token=credential", nil),
		httptest.NewRequest(http.MethodPost, "https://evidence.example/mcp", nil),
	}
	requests[1].AddCookie(&http.Cookie{Name: "access_token", Value: "credential"})
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", recorder.Code)
		}
	}
	if called {
		t.Fatal("protected handler was called")
	}
}

func TestBearerHeaderRejectsAmbiguousValues(t *testing.T) {
	requests := []*http.Request{
		httptest.NewRequest(http.MethodPost, "https://evidence.example/mcp", nil),
		httptest.NewRequest(http.MethodPost, "https://evidence.example/mcp", nil),
		httptest.NewRequest(http.MethodPost, "https://evidence.example/mcp", nil),
	}
	requests[0].Header["Authorization"] = []string{"Bearer one", "Bearer two"}
	requests[1].Header.Set("Authorization", "Basic credential")
	requests[2].Header.Set("Authorization", "Bearer token with spaces")
	for _, request := range requests {
		if _, err := bearerCredential(request); err == nil {
			t.Fatalf("bearerCredential(%q) error = nil", request.Header.Values("Authorization"))
		}
	}
}

func TestValidateBearerRequesterRejectsInvalidClaims(t *testing.T) {
	registry := httpTestRegistry(t)
	client, err := registry.GetClient(context.Background(), "client")
	if err != nil {
		t.Fatal(err)
	}
	removedRegistry, err := ParseClientRegistry([]byte(`{"version":1,"clients":[]}`), testResource)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		use       fosite.TokenUse
		scopes    []string
		audiences []string
		session   *Session
		clients   *ClientRegistry
	}{
		{name: "wrong token use", use: fosite.RefreshToken, scopes: []string{"mcp:read"}, audiences: []string{testResource}, session: NewSession(42, 3, testResource), clients: registry},
		{name: "missing scope", use: fosite.AccessToken, audiences: []string{testResource}, session: NewSession(42, 3, testResource), clients: registry},
		{name: "extra scope", use: fosite.AccessToken, scopes: []string{"mcp:read", "other"}, audiences: []string{testResource}, session: NewSession(42, 3, testResource), clients: registry},
		{name: "missing audience", use: fosite.AccessToken, scopes: []string{"mcp:read"}, session: NewSession(42, 3, testResource), clients: registry},
		{name: "extra audience", use: fosite.AccessToken, scopes: []string{"mcp:read"}, audiences: []string{testResource, "https://other.example/mcp"}, session: NewSession(42, 3, testResource), clients: registry},
		{name: "wrong session resource", use: fosite.AccessToken, scopes: []string{"mcp:read"}, audiences: []string{testResource}, session: NewSession(42, 3, "https://other.example/mcp"), clients: registry},
		{name: "zero account", use: fosite.AccessToken, scopes: []string{"mcp:read"}, audiences: []string{testResource}, session: NewSession(0, 3, testResource), clients: registry},
		{name: "zero epoch", use: fosite.AccessToken, scopes: []string{"mcp:read"}, audiences: []string{testResource}, session: NewSession(42, 0, testResource), clients: registry},
		{name: "removed client", use: fosite.AccessToken, scopes: []string{"mcp:read"}, audiences: []string{testResource}, session: NewSession(42, 3, testResource), clients: removedRegistry},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requester := fosite.NewAccessRequest(test.session)
			requester.Client = client
			for _, scope := range test.scopes {
				requester.GrantScope(scope)
			}
			for _, audience := range test.audiences {
				requester.GrantAudience(audience)
			}
			server := &Server{clients: test.clients, cfg: Config{Resource: testResource}}
			if err := server.validateBearerRequester(context.Background(), test.use, requester); !errors.Is(err, fosite.ErrInactiveToken) {
				t.Fatalf("validateBearerRequester() error = %v, want fosite.ErrInactiveToken", err)
			}
		})
	}
}
