package oauthserver

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/ory/fosite"
)

// Protect requires a current resource-bound access token before calling next.
func (s *Server) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noStoreOAuth(w)
		token, err := bearerCredential(r)
		if err != nil {
			writeBearerFailure(w, http.StatusUnauthorized, "invalid_token")
			return
		}
		op, err := s.beginOperation(r.Context(), Binding{Purpose: PurposeIntrospection, Resource: s.cfg.Resource}, CredentialAccess, token)
		if err != nil {
			writeBearerFailure(w, http.StatusServiceUnavailable, "temporarily_unavailable")
			return
		}
		defer func() { _ = op.Rollback() }()
		use, requester, protocolErr := s.provider.IntrospectToken(op.Context(), token, fosite.AccessToken, NewSession(0, 0, s.cfg.Resource), "mcp:read")
		if protocolErr == nil {
			protocolErr = s.validateBearerRequester(op.Context(), use, requester)
		}
		outcome, finishErr := op.Finish(protocolErr)
		if finishErr != nil {
			writeBearerFailure(w, http.StatusServiceUnavailable, "temporarily_unavailable")
			return
		}
		if protocolErr != nil || outcome != OutcomeCommitted {
			writeBearerFailure(w, http.StatusUnauthorized, "invalid_token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func bearerCredential(r *http.Request) (string, error) {
	if r.URL.Query().Has("access_token") {
		return "", errors.New("query bearer tokens are not allowed")
	}
	if cookie, err := r.Cookie("access_token"); err == nil && cookie.Value != "" {
		return "", errors.New("cookie bearer tokens are not allowed")
	}
	values := r.Header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return "", errors.New("bearer authorization is required")
	}
	presented := strings.TrimPrefix(values[0], "Bearer ")
	if presented == "" || strings.ContainsAny(presented, " \t\r\n,") {
		return "", errors.New("bearer authorization is malformed")
	}
	if extracted := fosite.AccessTokenFromRequest(r); extracted == "" || extracted != presented {
		return "", errors.New("bearer authorization is malformed")
	}
	return extracted, nil
}

func (s *Server) validateBearerRequester(ctx context.Context, use fosite.TokenUse, requester fosite.AccessRequester) error {
	if use != fosite.AccessToken || requester == nil || requester.GetClient() == nil {
		return fosite.ErrInactiveToken
	}
	if _, err := s.clients.GetClient(ctx, requester.GetClient().GetID()); err != nil {
		return fosite.ErrInactiveToken
	}
	if !exactStringSet([]string(requester.GetGrantedScopes()), "mcp:read") ||
		!exactStringSet([]string(requester.GetGrantedAudience()), s.cfg.Resource) {
		return fosite.ErrInactiveToken
	}
	session, ok := requester.GetSession().(*Session)
	if !ok || session == nil || session.AccountID <= 0 || session.AccountEpoch < 1 || session.Resource != s.cfg.Resource {
		return fosite.ErrInactiveToken
	}
	return nil
}

func writeBearerFailure(w http.ResponseWriter, status int, name string) {
	noStoreOAuth(w)
	if status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer error="`+name+`"`)
	}
	http.Error(w, http.StatusText(status), status)
}
