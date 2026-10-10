package oauthserver

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"

	"github.com/ory/fosite"
)

var tokenFormKeys = map[string]struct{}{
	"grant_type":    {},
	"client_id":     {},
	"resource":      {},
	"code":          {},
	"code_verifier": {},
	"redirect_uri":  {},
	"refresh_token": {},
	"scope":         {},
}

var revocationFormKeys = map[string]struct{}{
	"client_id":       {},
	"resource":        {},
	"token":           {},
	"token_type_hint": {},
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	noStoreOAuth(w)
	form, err := parseOAuthForm(w, r, tokenFormKeys)
	if err != nil {
		oauthJSONError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !singleFormValue(form, "resource") || form.Get("resource") != s.cfg.Resource {
		oauthJSONError(w, http.StatusBadRequest, errInvalidTarget.ErrorField)
		return
	}
	if r.Header.Get("Authorization") != "" || !requiredOAuthForm(form, "grant_type", "client_id") {
		oauthJSONError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	binding := Binding{Purpose: PurposeTokenExchange, ClientID: form.Get("client_id"), Resource: s.cfg.Resource}
	kind, presented, err := tokenCredential(form)
	if err != nil {
		protocolError := fosite.ErrorToRFC6749Error(err).Sanitize()
		oauthJSONError(w, protocolError.CodeField, protocolError.ErrorField)
		return
	}
	op, err := s.beginOperation(r.Context(), binding, kind, presented)
	if err != nil {
		writeTokenBeginError(w, err)
		return
	}
	defer func() { _ = op.Rollback() }()
	requester, protocolErr := s.provider.NewAccessRequest(op.Context(), normalizedTokenRequest(r.WithContext(op.Context()), s.cfg.Resource), NewSession(0, 0, s.cfg.Resource))
	var responder fosite.AccessResponder
	if protocolErr == nil {
		responder, protocolErr = s.provider.NewAccessResponse(op.Context(), requester)
	}
	outcome, finishErr := op.Finish(protocolErr)
	if finishErr != nil {
		oauthJSONError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	if protocolErr != nil {
		s.provider.WriteAccessError(r.Context(), w, requester, protocolErr)
		return
	}
	if outcome != OutcomeCommitted {
		oauthJSONError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	s.provider.WriteAccessResponse(r.Context(), w, requester, responder)
}

func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	noStoreOAuth(w)
	form, err := parseOAuthForm(w, r, revocationFormKeys)
	if err != nil {
		oauthJSONError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !singleFormValue(form, "resource") || form.Get("resource") != s.cfg.Resource {
		oauthJSONError(w, http.StatusBadRequest, errInvalidTarget.ErrorField)
		return
	}
	if r.Header.Get("Authorization") != "" || !requiredOAuthForm(form, "client_id", "token") {
		oauthJSONError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	binding := Binding{Purpose: PurposeRevocation, ClientID: form.Get("client_id"), Resource: s.cfg.Resource}
	op, err := s.beginOperation(r.Context(), binding, CredentialAccessOrRefresh, form.Get("token"))
	if err != nil {
		writeTokenBeginError(w, err)
		return
	}
	defer func() { _ = op.Rollback() }()
	protocolErr := s.provider.NewRevocationRequest(op.Context(), r.WithContext(op.Context()))
	outcome, finishErr := op.Finish(protocolErr)
	if finishErr != nil {
		oauthJSONError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	if errors.Is(protocolErr, fosite.ErrTemporarilyUnavailable) || errors.Is(protocolErr, fosite.ErrServerError) {
		oauthJSONError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	if protocolErr == nil && outcome != OutcomeCommitted {
		oauthJSONError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
		return
	}
	s.provider.WriteRevocationResponse(r.Context(), w, protocolErr)
}

func writeTokenBeginError(w http.ResponseWriter, err error) {
	if errors.Is(err, fosite.ErrAccessDenied) {
		oauthJSONError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	oauthJSONError(w, http.StatusServiceUnavailable, "temporarily_unavailable")
}

func tokenCredential(form url.Values) (CredentialKind, string, error) {
	switch form.Get("grant_type") {
	case "authorization_code":
		if !requiredOAuthForm(form, "code", "code_verifier", "redirect_uri") || hasOAuthFormField(form, "refresh_token") || hasOAuthFormField(form, "scope") {
			return CredentialNone, "", fmt.Errorf("authorization code form is invalid: %w", fosite.ErrInvalidRequest)
		}
		return CredentialCode, form.Get("code"), nil
	case "refresh_token":
		if !requiredOAuthForm(form, "refresh_token") || hasOAuthFormField(form, "code") || hasOAuthFormField(form, "code_verifier") || hasOAuthFormField(form, "redirect_uri") {
			return CredentialNone, "", fmt.Errorf("refresh token form is invalid: %w", fosite.ErrInvalidRequest)
		}
		if scope, present := form["scope"]; present && (len(scope) != 1 || scope[0] != "mcp:read") {
			return CredentialNone, "", fmt.Errorf("refresh token scope is invalid: %w", fosite.ErrInvalidScope)
		}
		return CredentialRefresh, form.Get("refresh_token"), nil
	default:
		return CredentialNone, "", fmt.Errorf("grant type is unsupported: %w", fosite.ErrUnsupportedGrantType)
	}
}

func parseOAuthForm(w http.ResponseWriter, r *http.Request, allowed map[string]struct{}) (url.Values, error) {
	if r.URL.RawQuery != "" {
		return nil, errors.New("query parameters are not allowed")
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		return nil, errors.New("content type is invalid")
	}
	r.Body = http.MaxBytesReader(w, r.Body, oauthFormBodyLimit)
	if err := r.ParseForm(); err != nil || len(r.PostForm) == 0 {
		return nil, errors.New("form is invalid")
	}
	for key, values := range r.PostForm {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			return nil, errors.New("form field is invalid")
		}
	}
	return r.PostForm, nil
}

func requiredOAuthForm(form url.Values, keys ...string) bool {
	for _, key := range keys {
		if !singleFormValue(form, key) {
			return false
		}
	}
	return true
}

func hasOAuthFormField(form url.Values, key string) bool {
	_, present := form[key]
	return present
}

func normalizedTokenRequest(parent *http.Request, resource string) *http.Request {
	clone := parent.Clone(parent.Context())
	clone.PostForm = cloneForm(parent.PostForm)
	clone.PostForm.Set("audience", resource)
	clone.Form = cloneForm(parent.Form)
	clone.Form.Set("audience", resource)
	return clone
}
