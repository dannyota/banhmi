package identity

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLoginCreatesIndependentSecretsAndSecureCookie(t *testing.T) {
	store := newMemoryStore()
	provider := &fakeProvider{authURL: "https://accounts.google.test/auth"}
	m := testManager(t, store, provider)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://evidence.example/auth/google/start?next=/connect", nil)

	m.handleLogin(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusFound)
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("Parse(Location) error = %v", err)
	}
	state := location.Query().Get("state")
	nonce := location.Query().Get("nonce")
	challenge := location.Query().Get("code_challenge")
	if state == "" || nonce == "" || challenge == "" || state == nonce || state == challenge || nonce == challenge {
		t.Fatalf("state, nonce, and challenge are not independent: %q %q %q", state, nonce, challenge)
	}
	loginCookie := responseCookie(t, recorder, LoginCookieName)
	assertSecureHostCookie(t, loginCookie)
	if store.login.ReturnPath != "/connect" || store.login.ExpiresAt.Sub(m.now()) != loginLifetime {
		t.Fatalf("stored login = %+v", store.login)
	}
}

func TestRegisterLeavesWebsiteLoginRouteAvailable(t *testing.T) {
	m := testManager(t, newMemoryStore(), &fakeProvider{authURL: "https://accounts.google.test/auth"})
	mux := http.NewServeMux()
	m.Register(mux)
	mux.HandleFunc("GET /login", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://evidence.example/login", nil))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("website login status = %d", recorder.Code)
	}
}

func TestPublicLoginRestrictsReturnPath(t *testing.T) {
	store := newMemoryStore()
	m := testManager(t, store, &fakeProvider{authURL: "https://accounts.google.test/auth"})
	recorder := httptest.NewRecorder()
	m.handleLogin(recorder, httptest.NewRequest(http.MethodGet, "https://evidence.example/auth/google/start?next=https://attacker.invalid", nil))
	if store.login.ReturnPath != "/app" {
		t.Fatalf("return path = %q, want /app", store.login.ReturnPath)
	}
}

func TestCallbackIssuesHashedSessionAndConsumesLoginOnce(t *testing.T) {
	store := newMemoryStore()
	provider := &fakeProvider{rawIDToken: "id-token", claims: providerClaims{
		Issuer: googleIssuer, Subject: "Subject", Email: "person@example.com",
		EmailVerified: true, DisplayName: "Person",
	}}
	m := testManager(t, store, provider)
	loginResponse := httptest.NewRecorder()
	m.BeginLogin(loginResponse, httptest.NewRequest(http.MethodGet, "https://evidence.example/login", nil), "/app", nil)
	location, _ := url.Parse(loginResponse.Header().Get("Location"))
	provider.claims.Nonce = location.Query().Get("nonce")
	loginCookie := responseCookie(t, loginResponse, LoginCookieName)

	callbackURL := "https://evidence.example/auth/google/callback?code=code&state=" + url.QueryEscape(location.Query().Get("state"))
	request := httptest.NewRequest(http.MethodGet, callbackURL, nil)
	request.AddCookie(loginCookie)
	recorder := httptest.NewRecorder()
	m.handleCallback(recorder, request)

	if recorder.Code != http.StatusFound || recorder.Header().Get("Location") != "/app" {
		t.Fatalf("callback status = %d, location = %q", recorder.Code, recorder.Header().Get("Location"))
	}
	sessionCookie := responseCookie(t, recorder, SessionCookieName)
	assertSecureHostCookie(t, sessionCookie)
	if sessionCookie.MaxAge != int(sessionLifetime/time.Second) || !sessionCookie.Expires.Equal(m.now().Add(sessionLifetime)) {
		t.Fatalf("session cookie MaxAge = %d, Expires = %v", sessionCookie.MaxAge, sessionCookie.Expires)
	}
	if !store.completeIssuedAt.Equal(m.now()) || !store.completeExpiresAt.Equal(m.now().Add(sessionLifetime)) {
		t.Fatalf("stored session issued = %v, expires = %v", store.completeIssuedAt, store.completeExpiresAt)
	}
	rawSession, err := decodeToken(sessionCookie.Value)
	if err != nil {
		t.Fatalf("decodeToken(session) error = %v", err)
	}
	if !equalBytes(store.sessions[sha256.Sum256(rawSession)].SessionHash, hash(rawSession)) {
		t.Fatal("CompleteLogin() did not receive only the session hash")
	}

	replay := httptest.NewRecorder()
	m.handleCallback(replay, request)
	if replay.Code != http.StatusUnauthorized || store.completeCount != 1 {
		t.Fatalf("replay status = %d, complete count = %d", replay.Code, store.completeCount)
	}
}

func TestCallbackRejectsNonceAndUnverifiedEmail(t *testing.T) {
	tests := []struct {
		name       string
		claims     providerClaims
		validNonce bool
	}{
		{name: "nonce", claims: providerClaims{Issuer: googleIssuer, Subject: "subject", Email: "person@example.com", EmailVerified: true, Nonce: "wrong"}},
		{name: "unverified email", claims: providerClaims{Issuer: googleIssuer, Subject: "subject", Email: "person@example.com", EmailVerified: false}, validNonce: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			provider := &fakeProvider{rawIDToken: "id-token", claims: test.claims}
			m := testManager(t, store, provider)
			login := httptest.NewRecorder()
			m.BeginLogin(login, httptest.NewRequest(http.MethodGet, "https://evidence.example/login", nil), "/app", nil)
			location, _ := url.Parse(login.Header().Get("Location"))
			if test.validNonce {
				provider.claims.Nonce = location.Query().Get("nonce")
			}
			request := httptest.NewRequest(http.MethodGet, "https://evidence.example/auth/google/callback?code=code&state="+url.QueryEscape(location.Query().Get("state")), nil)
			request.AddCookie(responseCookie(t, login, LoginCookieName))
			recorder := httptest.NewRecorder()
			m.handleCallback(recorder, request)
			if recorder.Code != http.StatusUnauthorized || store.completeCount != 0 || !store.loginConsumed {
				t.Fatalf("status = %d, complete = %d, consumed = %t", recorder.Code, store.completeCount, store.loginConsumed)
			}
			if strings.Contains(recorder.Body.String(), "person@example.com") {
				t.Fatal("callback response disclosed email")
			}
		})
	}
}

func TestCallbackConsumesTransactionOnProviderDenialAndFailure(t *testing.T) {
	tests := []struct {
		name       string
		query      func(string) string
		exchangeErr error
		verifyErr   error
	}{
		{
			name: "provider denial",
			query: func(state string) string {
				return "error=access_denied&state=" + url.QueryEscape(state)
			},
		},
		{
			name:        "exchange failure",
			exchangeErr: errors.New("exchange failed"),
		},
		{
			name:      "verification failure",
			verifyErr: errors.New("verification failed"),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			provider := &fakeProvider{
				rawIDToken: "id-token", exchangeErr: test.exchangeErr, verifyErr: test.verifyErr,
				claims: providerClaims{Issuer: googleIssuer, Subject: "subject", Email: "person@example.com", EmailVerified: true},
			}
			m := testManager(t, store, provider)
			login := httptest.NewRecorder()
			m.BeginLogin(login, httptest.NewRequest(http.MethodGet, "https://evidence.example/login", nil), "/app", nil)
			location, _ := url.Parse(login.Header().Get("Location"))
			provider.claims.Nonce = location.Query().Get("nonce")
			query := "code=code&state=" + url.QueryEscape(location.Query().Get("state"))
			if test.query != nil {
				query = test.query(location.Query().Get("state"))
			}
			request := httptest.NewRequest(http.MethodGet, "https://evidence.example/auth/google/callback?"+query, nil)
			request.AddCookie(responseCookie(t, login, LoginCookieName))

			first := httptest.NewRecorder()
			m.handleCallback(first, request)
			if first.Code != http.StatusUnauthorized || !store.loginConsumed || store.completeCount != 0 {
				t.Fatalf("first status = %d, consumed = %t, complete = %d", first.Code, store.loginConsumed, store.completeCount)
			}

			replay := httptest.NewRecorder()
			m.handleCallback(replay, request)
			if replay.Code != http.StatusUnauthorized || store.completeCount != 0 {
				t.Fatalf("replay status = %d, complete = %d", replay.Code, store.completeCount)
			}
		})
	}
}

func TestCallbackRejectsWrongStateBrowserAndExpiry(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*memoryStore, *http.Request)
	}{
		{name: "state", mutate: func(_ *memoryStore, r *http.Request) { r.URL.RawQuery = "code=code&state=wrong" }},
		{name: "browser", mutate: func(_ *memoryStore, r *http.Request) { r.Header.Set("Cookie", LoginCookieName+"="+encodeToken(make([]byte, randomValueSize))) }},
		{name: "expiry", mutate: func(store *memoryStore, _ *http.Request) { store.login.ExpiresAt = time.Unix(1, 0) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			provider := &fakeProvider{rawIDToken: "id-token", claims: providerClaims{Issuer: googleIssuer, Subject: "subject", Email: "person@example.com", EmailVerified: true}}
			m := testManager(t, store, provider)
			login := httptest.NewRecorder()
			m.BeginLogin(login, httptest.NewRequest(http.MethodGet, "https://evidence.example/login", nil), "/app", nil)
			location, _ := url.Parse(login.Header().Get("Location"))
			provider.claims.Nonce = location.Query().Get("nonce")
			request := httptest.NewRequest(http.MethodGet, "https://evidence.example/auth/google/callback?code=code&state="+url.QueryEscape(location.Query().Get("state")), nil)
			request.AddCookie(responseCookie(t, login, LoginCookieName))
			test.mutate(store, request)
			recorder := httptest.NewRecorder()
			m.handleCallback(recorder, request)
			if recorder.Code != http.StatusUnauthorized || store.completeCount != 0 {
				t.Fatalf("status = %d, complete = %d", recorder.Code, store.completeCount)
			}
		})
	}
}

func TestCallbackRotatesExistingSession(t *testing.T) {
	store := newMemoryStore()
	oldRaw := []byte("0123456789abcdef0123456789abcdef")
	store.sessions[sha256.Sum256(oldRaw)] = Principal{AccountID: 42, SessionHash: hash(oldRaw)}
	provider := &fakeProvider{rawIDToken: "id-token", claims: providerClaims{Issuer: googleIssuer, Subject: "subject", Email: "person@example.com", EmailVerified: true}}
	m := testManager(t, store, provider)
	login := httptest.NewRecorder()
	m.BeginLogin(login, httptest.NewRequest(http.MethodGet, "https://evidence.example/login", nil), "/app", nil)
	location, _ := url.Parse(login.Header().Get("Location"))
	provider.claims.Nonce = location.Query().Get("nonce")
	request := httptest.NewRequest(http.MethodGet, "https://evidence.example/auth/google/callback?code=code&state="+url.QueryEscape(location.Query().Get("state")), nil)
	request.AddCookie(responseCookie(t, login, LoginCookieName))
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: encodeToken(oldRaw)})
	recorder := httptest.NewRecorder()
	m.handleCallback(recorder, request)
	if recorder.Code != http.StatusFound || store.revokeCount != 1 || store.completeCount != 1 {
		t.Fatalf("status = %d, revokes = %d, completes = %d", recorder.Code, store.revokeCount, store.completeCount)
	}
}

func TestCallbackRequiresExactPendingConsentCookie(t *testing.T) {
	store := newMemoryStore()
	provider := &fakeProvider{rawIDToken: "id-token", claims: providerClaims{Issuer: googleIssuer, Subject: "subject", Email: "person@example.com", EmailVerified: true}}
	m := testManager(t, store, provider)
	pending := []byte("pending-cookie-value")
	login := httptest.NewRecorder()
	m.BeginLogin(login, httptest.NewRequest(http.MethodGet, "https://evidence.example/login", nil), "/oauth/resume", hash(pending))
	location, _ := url.Parse(login.Header().Get("Location"))
	provider.claims.Nonce = location.Query().Get("nonce")
	request := httptest.NewRequest(http.MethodGet, "https://evidence.example/auth/google/callback?code=code&state="+url.QueryEscape(location.Query().Get("state")), nil)
	request.AddCookie(responseCookie(t, login, LoginCookieName))
	request.AddCookie(&http.Cookie{Name: PendingConsentCookieName, Value: encodeToken([]byte("swapped-cookie-value"))})
	recorder := httptest.NewRecorder()
	m.handleCallback(recorder, request)
	if recorder.Code != http.StatusUnauthorized || store.completeCount != 0 {
		t.Fatalf("status = %d, complete = %d", recorder.Code, store.completeCount)
	}
}

func TestLogoutRequiresOriginCSRFAndRevokesSession(t *testing.T) {
	store := newMemoryStore()
	m := testManager(t, store, nil)
	raw := []byte("0123456789abcdef0123456789abcdef")
	store.sessions[sha256.Sum256(raw)] = Principal{AccountID: 42, SessionHash: hash(raw)}
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/logout", strings.NewReader("csrf_token="+url.QueryEscape(m.csrfForSession(raw))))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://evidence.example")
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: encodeToken(raw)})
	recorder := httptest.NewRecorder()
	m.handleLogout(recorder, request)
	if recorder.Code != http.StatusNoContent || store.revokeCount != 1 {
		t.Fatalf("status = %d, revoke count = %d", recorder.Code, store.revokeCount)
	}

	wrongOrigin := httptest.NewRequest(http.MethodPost, "https://evidence.example/logout", nil)
	wrongOrigin.Header.Set("Origin", "https://attacker.invalid")
	wrongOrigin.AddCookie(&http.Cookie{Name: SessionCookieName, Value: encodeToken(raw)})
	wrongOrigin.Header.Set(csrfHeader, m.CSRFToken(wrongOrigin))
	denied := httptest.NewRecorder()
	m.handleLogout(denied, wrongOrigin)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("wrong-origin status = %d", denied.Code)
	}
}

func TestStorageFailuresReturnServiceUnavailable(t *testing.T) {
	store := newMemoryStore()
	store.createErr = errors.New("database unavailable")
	m := testManager(t, store, &fakeProvider{authURL: "https://accounts.google.test/auth"})
	recorder := httptest.NewRecorder()
	m.handleLogin(recorder, httptest.NewRequest(http.MethodGet, "https://evidence.example/login", nil))
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "database") {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
}

func responseCookie(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response has no %s cookie", name)
	return nil
}

func assertSecureHostCookie(t *testing.T, cookie *http.Cookie) {
	t.Helper()
	if !cookie.Secure || !cookie.HttpOnly || cookie.Path != "/" || cookie.Domain != "" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie attributes = %+v", cookie)
	}
}

