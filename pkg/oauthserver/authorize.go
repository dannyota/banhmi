package oauthserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ory/fosite"

	"danny.vn/banhmi/pkg/identity"
)

const pendingConsentPurpose = "oauth-pending-consent-v1"

const maxAuthorizeQuerySize = 16 << 10

var consentPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex,nofollow"><title>Authorize connection</title></head>
<body><main><h1>Authorize {{.ClientName}}</h1><p>{{.ClientName}} requests access to read evidence.</p>
<dl><dt>Redirect host</dt><dd>{{.RedirectHost}}</dd><dt>Resource</dt><dd>{{.Resource}}</dd><dt>Scope</dt><dd>{{.Scope}}</dd></dl>
<form method="post" action="/oauth/authorize/decision"><input type="hidden" name="csrf_token" value="{{.CSRF}}">
<button type="submit" name="decision" value="approve">Authorize</button><button type="submit" name="decision" value="deny">Deny</button></form></main></body></html>`))

type consentView struct {
	ClientName   string
	RedirectHost string
	Resource     string
	Scope        string
	CSRF         string
}

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	noStoreOAuth(w)
	if len(r.URL.RawQuery) > maxAuthorizeQuerySize || !utf8.ValidString(r.URL.RawQuery) {
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	values, parseErr := url.ParseQuery(r.URL.RawQuery)
	if parseErr != nil || !validAuthorizeUTF8(values) {
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	pending, err := validateAuthorizeParameters(values, s.cfg.Resource)
	if err != nil {
		s.writeInitialAuthorizeError(w, r, err)
		return
	}
	if err := s.clients.ValidateRedirect(pending.ClientID, pending.RedirectURI); err != nil {
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	fositeRequest := normalizedAuthorizeRequest(r, values, s.cfg.Resource)
	requester, err := s.provider.NewAuthorizeRequest(r.Context(), fositeRequest)
	if err != nil {
		s.writeAuthorizeErrorRedirect(w, requester, err)
		return
	}
	payload, err := encodePendingAuthorization(values)
	if err != nil {
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	ciphertext, err := s.sessions.Seal(pendingConsentPurpose, payload)
	if err != nil {
		oauthFailure(w, http.StatusServiceUnavailable)
		return
	}
	handle, err := randomOAuthHandle()
	if err != nil {
		oauthFailure(w, http.StatusServiceUnavailable)
		return
	}
	handleHash := hashOAuthHandle(handle)
	expiresAt := s.now().Add(s.cfg.ConsentLifetime)
	if err := s.createPendingConsent(r.Context(), handleHash, ciphertext, expiresAt); err != nil {
		oauthFailure(w, http.StatusServiceUnavailable)
		return
	}
	setPendingConsentCookie(w, handle, expiresAt)
	principal, err := s.sessions.Authenticate(r)
	if errors.Is(err, identity.ErrUnauthenticated) {
		s.sessions.BeginLogin(w, r, "/oauth/resume", handleHash)
		return
	}
	if err != nil {
		writeIdentityFailure(w, err)
		return
	}
	stored, err := s.bindPendingConsent(r.Context(), handleHash, principal.SessionHash, s.now())
	if err != nil {
		oauthFailure(w, http.StatusServiceUnavailable)
		return
	}
	if err := s.renderConsent(w, r, stored, principal); err != nil {
		oauthFailure(w, http.StatusBadRequest)
	}
}

func validAuthorizeUTF8(values url.Values) bool {
	for key, entries := range values {
		if !utf8.ValidString(key) {
			return false
		}
		for _, value := range entries {
			if !utf8.ValidString(value) {
				return false
			}
		}
	}
	return true
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	noStoreOAuth(w)
	handle, handleHash, err := pendingConsentHandle(r)
	_ = handle
	if err != nil {
		oauthFailure(w, http.StatusUnauthorized)
		return
	}
	principal, err := s.sessions.Authenticate(r)
	if err != nil {
		writeIdentityFailure(w, err)
		return
	}
	stored, err := s.bindPendingConsent(r.Context(), handleHash, principal.SessionHash, s.now())
	if err != nil {
		if errors.Is(err, errStorageFailure) {
			oauthFailure(w, http.StatusServiceUnavailable)
			return
		}
		oauthFailure(w, http.StatusUnauthorized)
		return
	}
	if err := s.renderConsent(w, r, stored, principal); err != nil {
		oauthFailure(w, http.StatusBadRequest)
	}
}

func (s *Server) handleDecision(w http.ResponseWriter, r *http.Request) {
	noStoreOAuth(w)
	if r.Header.Get("Origin") != s.cfg.Issuer {
		oauthFailure(w, http.StatusForbidden)
		return
	}
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/x-www-form-urlencoded" || r.URL.RawQuery != "" {
		oauthFailure(w, http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, oauthFormBodyLimit)
	if err := r.ParseForm(); err != nil || !singleFormValue(r.PostForm, "csrf_token") || !s.sessions.ValidCSRF(r, r.PostForm.Get("csrf_token")) {
		oauthFailure(w, http.StatusForbidden)
		return
	}
	if len(r.PostForm) != 2 || !singleFormValue(r.PostForm, "decision") {
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	decision := ConsentDecision(0)
	switch r.PostForm.Get("decision") {
	case "approve":
		decision = ConsentApprove
	case "deny":
		decision = ConsentDeny
	default:
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	_, handleHash, err := pendingConsentHandle(r)
	if err != nil {
		oauthFailure(w, http.StatusUnauthorized)
		return
	}
	principal, err := s.sessions.Authenticate(r)
	if err != nil {
		writeIdentityFailure(w, err)
		return
	}
	preRead, err := s.readPendingConsent(r.Context(), handleHash, principal.SessionHash, s.now())
	if err != nil {
		if errors.Is(err, errStorageFailure) {
			oauthFailure(w, http.StatusServiceUnavailable)
			return
		}
		oauthFailure(w, http.StatusUnauthorized)
		return
	}
	preRequest, _, err := s.openPendingRequest(r, preRead)
	if err != nil {
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	binding := Binding{
		Purpose: PurposeAuthorize, ClientID: preRequest.GetClient().GetID(),
		Resource: s.cfg.Resource, AccountID: principal.AccountID,
	}
	op, err := s.beginOperation(r.Context(), binding, CredentialNone, "")
	if err != nil {
		writeProtocolStorageFailure(w, err)
		return
	}
	defer func() { _ = op.Rollback() }()
	stored, err := s.consumePendingConsent(op.Context(), handleHash, principal.SessionHash, s.now(), decision)
	var requester fosite.AuthorizeRequester
	if err == nil {
		requester, _, err = s.openPendingRequest(r.WithContext(op.Context()), stored)
	}
	var responder fosite.AuthorizeResponder
	protocolErr := err
	if protocolErr == nil && requester.GetClient().GetID() != binding.ClientID {
		protocolErr = fosite.ErrAccessDenied
	}
	if protocolErr == nil && decision == ConsentDeny {
		protocolErr = fosite.ErrAccessDenied
	}
	if protocolErr == nil {
		requester.GrantScope("mcp:read")
		requester.GrantAudience(s.cfg.Resource)
		responder, protocolErr = s.provider.NewAuthorizeResponse(op.Context(), requester, NewSession(principal.AccountID, principal.AuthorizationEpoch, s.cfg.Resource))
	}
	outcome, finishErr := op.Finish(protocolErr)
	if finishErr != nil {
		oauthFailure(w, http.StatusServiceUnavailable)
		return
	}
	if errors.Is(protocolErr, errStorageFailure) {
		oauthFailure(w, http.StatusServiceUnavailable)
		return
	}
	if outcome == OutcomeDeniedCommitted {
		clearPendingConsentCookie(w)
		s.writeAuthorizeErrorRedirect(w, requester, fosite.ErrAccessDenied)
		return
	}
	if protocolErr != nil {
		if requester != nil {
			s.writeAuthorizeErrorRedirect(w, requester, protocolErr)
		} else {
			oauthFailure(w, http.StatusBadRequest)
		}
		return
	}
	if outcome != OutcomeCommitted {
		oauthFailure(w, http.StatusServiceUnavailable)
		return
	}
	clearPendingConsentCookie(w)
	responder.AddParameter("iss", s.cfg.Issuer)
	s.provider.WriteAuthorizeResponse(r.Context(), w, requester, responder)
}

func (s *Server) renderConsent(w http.ResponseWriter, r *http.Request, stored PendingConsent, _ identity.Principal) error {
	requester, pending, err := s.openPendingRequest(r, stored)
	if err != nil {
		return err
	}
	client, exists := s.clients.clients[requester.GetClient().GetID()]
	if !exists {
		return fosite.ErrInvalidClient
	}
	redirect, err := url.Parse(pending.RedirectURI)
	if err != nil {
		return err
	}
	csrf := s.sessions.CSRFToken(r)
	if csrf == "" {
		return identity.ErrUnauthenticated
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")
	return consentPage.Execute(w, consentView{
		ClientName: client.ClientName, RedirectHost: redirect.Host, Resource: pending.Resource,
		Scope: pending.Scope, CSRF: csrf,
	})
}

func (s *Server) openPendingRequest(parent *http.Request, stored PendingConsent) (fosite.AuthorizeRequester, pendingAuthorization, error) {
	plain, err := s.sessions.Open(pendingConsentPurpose, stored.RequestCiphertext)
	if err != nil {
		return nil, pendingAuthorization{}, err
	}
	request, err := decodePendingAuthorization(plain)
	if err != nil {
		return nil, pendingAuthorization{}, err
	}
	request = request.WithContext(parent.Context())
	pending, err := validateAuthorizeParameters(request.URL.Query(), s.cfg.Resource)
	if err != nil {
		return nil, pendingAuthorization{}, err
	}
	if err := s.clients.ValidateRedirect(pending.ClientID, pending.RedirectURI); err != nil {
		return nil, pendingAuthorization{}, err
	}
	requester, err := s.provider.NewAuthorizeRequest(request.Context(), normalizedAuthorizeRequest(request, request.URL.Query(), s.cfg.Resource))
	return requester, pending, err
}

func normalizedAuthorizeRequest(parent *http.Request, values url.Values, resource string) *http.Request {
	query := cloneForm(values)
	if query.Get("response_mode") == "query" {
		query.Del("response_mode")
	}
	query.Set("audience", resource)
	clone := parent.Clone(parent.Context())
	clone.URL.RawQuery = query.Encode()
	return clone
}

func (s *Server) writeInitialAuthorizeError(w http.ResponseWriter, r *http.Request, protocolErr error) {
	values := r.URL.Query()
	if len(values["client_id"]) != 1 || len(values["redirect_uri"]) != 1 || s.clients.ValidateRedirect(values.Get("client_id"), values.Get("redirect_uri")) != nil {
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	state := ""
	if len(values["state"]) == 1 {
		state = values.Get("state")
	}
	s.writeRawAuthorizeErrorRedirect(w, values.Get("redirect_uri"), state, protocolErr)
}

func (s *Server) writeAuthorizeErrorRedirect(w http.ResponseWriter, requester fosite.AuthorizeRequester, protocolErr error) {
	if requester == nil || requester.GetClient() == nil || !requester.IsRedirectURIValid() || requester.GetRedirectURI() == nil || s.clients.ValidateRedirect(requester.GetClient().GetID(), requester.GetRedirectURI().String()) != nil {
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	s.writeRawAuthorizeErrorRedirect(w, requester.GetRedirectURI().String(), requester.GetState(), protocolErr)
}

func (s *Server) writeRawAuthorizeErrorRedirect(w http.ResponseWriter, rawRedirect, state string, protocolErr error) {
	noStoreOAuth(w)
	redirect, err := url.Parse(rawRedirect)
	if err != nil {
		oauthFailure(w, http.StatusBadRequest)
		return
	}
	rfcError := fosite.ErrorToRFC6749Error(protocolErr).Sanitize()
	query := redirect.Query()
	query.Set("error", rfcError.ErrorField)
	query.Set("error_description", rfcError.DescriptionField)
	query.Set("iss", s.cfg.Issuer)
	if state != "" {
		query.Set("state", state)
	}
	redirect.RawQuery = query.Encode()
	w.Header().Set("Location", redirect.String())
	w.WriteHeader(http.StatusSeeOther)
}

func randomOAuthHandle() ([]byte, error) {
	handle := make([]byte, 32)
	if _, err := rand.Read(handle); err != nil {
		return nil, err
	}
	return handle, nil
}

func hashOAuthHandle(handle []byte) []byte {
	sum := sha256.Sum256(handle)
	return sum[:]
}

func pendingConsentHandle(r *http.Request) ([]byte, []byte, error) {
	cookie, err := r.Cookie(identity.PendingConsentCookieName)
	if err != nil || strings.Contains(cookie.Value, "=") {
		return nil, nil, errors.New("pending consent cookie is missing")
	}
	handle, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(handle) != 32 {
		return nil, nil, errors.New("pending consent cookie is invalid")
	}
	return handle, hashOAuthHandle(handle), nil
}

func setPendingConsentCookie(w http.ResponseWriter, handle []byte, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: identity.PendingConsentCookieName, Value: base64.RawURLEncoding.EncodeToString(handle), Path: "/",
		Expires: expiresAt, MaxAge: int(defaultConsentLifetime / time.Second), Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func clearPendingConsentCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: identity.PendingConsentCookieName, Value: "", Path: "/", Expires: time.Unix(1, 0),
		MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
}

func singleFormValue(values url.Values, key string) bool {
	entries, exists := values[key]
	return exists && len(entries) == 1 && entries[0] != ""
}

func writeIdentityFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, identity.ErrUnauthenticated) {
		oauthFailure(w, http.StatusUnauthorized)
		return
	}
	if errors.Is(err, identity.ErrBlocked) {
		oauthFailure(w, http.StatusForbidden)
		return
	}
	oauthFailure(w, http.StatusServiceUnavailable)
}

func writeProtocolStorageFailure(w http.ResponseWriter, err error) {
	if errors.Is(err, fosite.ErrAccessDenied) {
		oauthFailure(w, http.StatusForbidden)
		return
	}
	oauthFailure(w, http.StatusServiceUnavailable)
}
