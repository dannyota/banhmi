package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"danny.vn/banhmi/pkg/identity"
	"danny.vn/banhmi/pkg/mcp"
	"danny.vn/banhmi/pkg/web"
)

type fakeRegistrar func(*http.ServeMux)

func (f fakeRegistrar) Register(mux *http.ServeMux) { f(mux) }

type fakeOAuthRoutes struct{}

func (fakeOAuthRoutes) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func (fakeOAuthRoutes) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer valid" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type fakeWebsiteAuth struct{}

func (fakeWebsiteAuth) Authenticate(r *http.Request) (identity.Principal, error) {
	if cookie, err := r.Cookie(identity.SessionCookieName); err == nil && cookie.Value == "valid" {
		return identity.Principal{Email: "person@example.com", DisplayName: "Person"}, nil
	}
	return identity.Principal{}, identity.ErrUnauthenticated
}

func (fakeWebsiteAuth) CSRFToken(*http.Request) string { return "csrf" }

func (fakeWebsiteAuth) ValidCSRF(*http.Request, string) bool { return true }

type fakeEvidence struct{}

func (fakeEvidence) Search(context.Context, mcp.SearchInput) (mcp.SearchOutput, error) {
	return mcp.SearchOutput{}, nil
}

func (fakeEvidence) Document(context.Context, mcp.DocumentInput) (mcp.DocumentOutput, error) {
	return mcp.DocumentOutput{}, nil
}

func TestCanonicalHostUsesExactRequestAuthority(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := canonicalHost(next, "evidence.example")

	for _, test := range []struct {
		name      string
		host      string
		forwarded string
		path      string
		want      int
	}{
		{name: "canonical", host: "evidence.example", want: http.StatusOK},
		{name: "unknown", host: "attacker.example", want: http.StatusMisdirectedRequest},
		{name: "forwarded canonical ignored", host: "attacker.example", forwarded: "evidence.example", want: http.StatusMisdirectedRequest},
		{name: "forwarded attacker ignored", host: "evidence.example", forwarded: "attacker.example", want: http.StatusOK},
		{name: "health bypass", host: "127.0.0.1:8088", path: "/healthz", want: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := test.path
			if path == "" {
				path = "/"
			}
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Host = test.host
			r.Header.Set("Forwarded", "host="+test.forwarded)
			r.Header.Set("X-Forwarded-Host", test.forwarded)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != test.want {
				t.Fatalf("status = %d, want %d", w.Code, test.want)
			}
		})
	}
}

func TestComposedRoutesKeepWebsiteAndMCPCredentialsSeparate(t *testing.T) {
	origin, _ := url.Parse("https://evidence.example")
	website, err := web.New(fakeEvidence{}, fakeWebsiteAuth{}, origin)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	identityRoutes := fakeRegistrar(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /auth/google/start", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusFound) })
	})
	evidence := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	registerHostedRoutes(mux, identityRoutes, fakeOAuthRoutes{}, website, evidence, testLogger())

	request := func(method, path string, headers map[string]string, cookie *http.Cookie) int {
		r := httptest.NewRequest(method, path, nil)
		for name, value := range headers {
			r.Header.Set(name, value)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}

	if got := request(http.MethodGet, "/", nil, nil); got != http.StatusOK {
		t.Fatalf("public landing status = %d", got)
	}
	if got := request(http.MethodGet, "/.well-known/oauth-authorization-server", nil, nil); got != http.StatusOK {
		t.Fatalf("public metadata status = %d", got)
	}
	if got := request(http.MethodPost, "/api/v1/search", map[string]string{"Authorization": "Bearer valid"}, nil); got != http.StatusUnauthorized {
		t.Fatalf("bearer-authorized website API status = %d, want 401", got)
	}
	if got := request(http.MethodPost, "/mcp", nil, &http.Cookie{Name: identity.SessionCookieName, Value: "valid"}); got != http.StatusUnauthorized {
		t.Fatalf("cookie-authorized MCP status = %d, want 401", got)
	}
	if got := request(http.MethodPost, "/mcp", map[string]string{"Authorization": "Bearer valid"}, nil); got != http.StatusNoContent {
		t.Fatalf("OAuth-authorized MCP status = %d, want 204", got)
	}
}
