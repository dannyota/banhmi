package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"time"
)

// Register adds Google login, callback, and logout routes to mux.
func (m *Manager) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/google/start", m.handleLogin)
	mux.HandleFunc("GET /auth/google/callback", m.handleCallback)
	mux.HandleFunc("POST /logout", m.handleLogout)
}

func (m *Manager) handleLogin(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if next != "/app" && next != "/connect" {
		next = "/app"
	}
	m.BeginLogin(w, r, next, nil)
}

// BeginLogin starts a Google login for a validated public or OAuth return path.
func (m *Manager) BeginLogin(w http.ResponseWriter, r *http.Request, next string, pendingConsentHash []byte) {
	noStore(w)
	if !validLoginDestination(next, pendingConsentHash) {
		genericError(w, http.StatusBadRequest)
		return
	}
	provider, err := m.loginProvider(r.Context())
	if err != nil {
		genericError(w, http.StatusServiceUnavailable)
		return
	}
	stateRaw, err := randomBytes(randomValueSize)
	if err != nil {
		genericError(w, http.StatusServiceUnavailable)
		return
	}
	browserRaw, err := randomBytes(randomValueSize)
	if err != nil {
		genericError(w, http.StatusServiceUnavailable)
		return
	}
	nonceRaw, err := randomBytes(randomValueSize)
	if err != nil {
		genericError(w, http.StatusServiceUnavailable)
		return
	}
	verifierRaw, err := randomBytes(randomValueSize)
	if err != nil {
		genericError(w, http.StatusServiceUnavailable)
		return
	}
	state := encodeToken(stateRaw)
	nonce := encodeToken(nonceRaw)
	verifier := encodeToken(verifierRaw)
	sealedVerifier, err := m.Seal(pkcePurpose, []byte(verifier))
	if err != nil {
		genericError(w, http.StatusServiceUnavailable)
		return
	}
	expiresAt := m.now().Add(loginLifetime)
	err = m.store.CreateLogin(r.Context(), LoginTransaction{
		StateHash:              hash([]byte(state)),
		BrowserHash:            hash(browserRaw),
		NonceHash:              hash([]byte(nonce)),
		PKCEVerifierCiphertext: sealedVerifier,
		ReturnPath:             next,
		PendingConsentHash:     append([]byte(nil), pendingConsentHash...),
		ExpiresAt:              expiresAt,
	})
	if err != nil {
		genericError(w, http.StatusServiceUnavailable)
		return
	}
	setCookie(w, LoginCookieName, encodeToken(browserRaw), expiresAt, int(loginLifetime/time.Second))
	http.Redirect(w, r, provider.AuthorizationURL(state, nonce, pkceChallenge(verifier)), http.StatusFound)
}

func (m *Manager) handleCallback(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	state := r.URL.Query().Get("state")
	loginCookie, err := r.Cookie(LoginCookieName)
	if err != nil || state == "" {
		genericError(w, http.StatusUnauthorized)
		return
	}
	browserRaw, err := decodeToken(loginCookie.Value)
	if err != nil || len(browserRaw) != randomValueSize {
		genericError(w, http.StatusUnauthorized)
		return
	}
	transaction, err := m.store.ConsumeLogin(r.Context(), hash([]byte(state)), hash(browserRaw), m.now())
	if err != nil {
		writeAuthError(w, err)
		return
	}
	clearCookie(w, LoginCookieName)
	if !m.validPendingConsent(r, transaction.PendingConsentHash) {
		genericError(w, http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		genericError(w, http.StatusUnauthorized)
		return
	}
	verifier, err := m.Open(pkcePurpose, transaction.PKCEVerifierCiphertext)
	if err != nil {
		genericError(w, http.StatusUnauthorized)
		return
	}
	provider, err := m.loginProvider(r.Context())
	if err != nil {
		genericError(w, http.StatusServiceUnavailable)
		return
	}
	exchangeContext, cancelExchange := context.WithTimeout(r.Context(), providerTimeout)
	rawIDToken, err := provider.Exchange(exchangeContext, r.URL.Query().Get("code"), string(verifier))
	cancelExchange()
	if err != nil {
		genericError(w, http.StatusUnauthorized)
		return
	}
	verifyContext, cancelVerify := context.WithTimeout(r.Context(), providerTimeout)
	claims, err := provider.Verify(verifyContext, rawIDToken)
	cancelVerify()
	if err != nil || claims.Issuer != googleIssuer || claims.Subject == "" ||
		claims.Email == "" || !claims.EmailVerified ||
		subtle.ConstantTimeCompare(hash([]byte(claims.Nonce)), transaction.NonceHash) != 1 {
		genericError(w, http.StatusUnauthorized)
		return
	}

	if oldCookie, cookieErr := r.Cookie(SessionCookieName); cookieErr == nil {
		if oldRaw, decodeErr := decodeToken(oldCookie.Value); decodeErr == nil && len(oldRaw) == randomValueSize {
			if err := m.store.RevokeSession(r.Context(), hash(oldRaw), m.now()); err != nil && !errors.Is(err, ErrUnauthenticated) {
				genericError(w, http.StatusServiceUnavailable)
				return
			}
		}
	}
	sessionRaw, err := randomBytes(randomValueSize)
	if err != nil {
		genericError(w, http.StatusServiceUnavailable)
		return
	}
	expiresAt := m.now().Add(sessionLifetime)
	_, err = m.store.CompleteLogin(r.Context(), Profile{
		Issuer: googleIssuer, Subject: claims.Subject, Email: claims.Email, DisplayName: claims.DisplayName,
	}, hash(sessionRaw), m.now(), expiresAt)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	setCookie(w, SessionCookieName, encodeToken(sessionRaw), expiresAt, int(sessionLifetime/time.Second))
	http.Redirect(w, r, transaction.ReturnPath, http.StatusFound)
}

func (m *Manager) validPendingConsent(r *http.Request, expected []byte) bool {
	if len(expected) == 0 {
		return true
	}
	cookie, err := r.Cookie(PendingConsentCookieName)
	if err != nil {
		return false
	}
	raw, err := decodeToken(cookie.Value)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(hash(raw), expected) == 1
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	token := r.Header.Get(csrfHeader)
	if token == "" {
		token = r.FormValue("csrf_token")
	}
	if r.Header.Get("Origin") != m.cfg.PublicOrigin.String() || !m.ValidCSRF(r, token) {
		genericError(w, http.StatusForbidden)
		return
	}
	principal, err := m.Authenticate(r)
	if err != nil {
		writeAuthError(w, err)
		return
	}
	if err := m.store.RevokeSession(r.Context(), principal.SessionHash, m.now()); err != nil {
		writeAuthError(w, err)
		return
	}
	clearCookie(w, SessionCookieName)
	w.WriteHeader(http.StatusNoContent)
}

func validLoginDestination(next string, pendingConsentHash []byte) bool {
	if len(pendingConsentHash) > 0 {
		return next == "/oauth/resume"
	}
	return next == "/app" || next == "/connect"
}

func setCookie(w http.ResponseWriter, name, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/", Expires: expires, MaxAge: maxAge,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, name string) {
	setCookie(w, name, "", time.Unix(1, 0), -1)
}

func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func genericError(w http.ResponseWriter, status int) {
	http.Error(w, http.StatusText(status), status)
}

func writeAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrUnauthenticated) || errors.Is(err, ErrBlocked) {
		genericError(w, http.StatusUnauthorized)
		return
	}
	genericError(w, http.StatusServiceUnavailable)
}

