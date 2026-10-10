package oauthserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"danny.vn/banhmi/pkg/identity"
)

const (
	flowIssuer   = "https://evidence.example"
	flowResource = flowIssuer + "/mcp"
	flowClientID = "flow-client"
	flowRedirect = "https://client.example/oauth/callback"
)

type flowHarness struct {
	t         *testing.T
	ctx       context.Context
	owner     *pgxpool.Pool
	runtime   *pgxpool.Pool
	manager   *identity.Manager
	server    *Server
	mux       *http.ServeMux
	principal identity.Principal
	session   *http.Cookie
}

type flowAuthorization struct {
	code string
}

type flowTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

func TestOAuthPostgresComposedFlowAndRefreshReplay(t *testing.T) {
	h := newFlowHarness(t)
	auth := h.authorize("state-main")

	wrong := h.exchangeCode(auth.code, strings.Repeat("x", len(flowVerifier())), flowRedirect)
	assertOAuthError(t, wrong, http.StatusBadRequest, "invalid_grant")

	tokens := h.exchangeCodeSuccess(auth.code, flowVerifier(), flowRedirect)
	h.assertPersistedAudience()
	h.assertProtected(h.server, tokens.AccessToken, http.StatusNoContent)
	h.assertBearerBoundary(tokens, auth.code)
	h.assertRemovedClientRejects(tokens.AccessToken)
	h.assertWrongResourceRejects(tokens.AccessToken)

	rotated := h.refresh(tokens.RefreshToken)
	h.assertProtected(h.server, rotated.AccessToken, http.StatusNoContent)

	replay := h.refreshResponse(tokens.RefreshToken)
	assertOAuthError(t, replay, http.StatusBadRequest, "invalid_grant")
	h.assertProtected(h.server, rotated.AccessToken, http.StatusUnauthorized)
	assertOAuthError(t, h.refreshResponse(rotated.RefreshToken), http.StatusBadRequest, "invalid_grant")
	h.assertReplayCommitted("refresh_replay")
}

func TestOAuthPostgresBlockAndUnblockNeverReviveCredentials(t *testing.T) {
	h := newFlowHarness(t)
	auth := h.authorize("state-block")
	tokens := h.exchangeCodeSuccess(auth.code, flowVerifier(), flowRedirect)
	pending := h.beginAuthorization("state-pending-block")

	if _, err := h.owner.Exec(h.ctx, "SELECT auth.block_user_account($1, $2)", h.principal.AccountID, "flow integration block"); err != nil {
		t.Fatalf("block_user_account() error = %v", err)
	}
	h.assertProtected(h.server, tokens.AccessToken, http.StatusUnauthorized)
	assertOAuthError(t, h.refreshResponse(tokens.RefreshToken), http.StatusBadRequest, "invalid_grant")
	h.assertPendingResume(pending, http.StatusForbidden)
	if _, err := h.manager.Authenticate(h.sessionRequest()); !errors.Is(err, identity.ErrBlocked) {
		t.Fatalf("Authenticate() after block error = %v, want ErrBlocked", err)
	}

	if _, err := h.owner.Exec(h.ctx, "SELECT auth.unblock_user_account($1, $2)", h.principal.AccountID, "flow integration unblock"); err != nil {
		t.Fatalf("unblock_user_account() error = %v", err)
	}
	h.assertProtected(h.server, tokens.AccessToken, http.StatusUnauthorized)
	assertOAuthError(t, h.refreshResponse(tokens.RefreshToken), http.StatusBadRequest, "invalid_grant")
	h.assertPendingResume(pending, http.StatusUnauthorized)
	if _, err := h.manager.Authenticate(h.sessionRequest()); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("Authenticate() after unblock error = %v, want ErrUnauthenticated", err)
	}

	var active bool
	if err := h.owner.QueryRow(h.ctx, `
		SELECT revoked_at IS NULL
		FROM auth.oauth_grant
		WHERE account_id = $1
		ORDER BY id DESC
		LIMIT 1`, h.principal.AccountID).Scan(&active); err != nil {
		t.Fatalf("query blocked grant: %v", err)
	}
	if active {
		t.Fatal("unblock restored a revoked OAuth grant")
	}
}

func TestOAuthPostgresAuthorizationCodeReplayRevokesFamily(t *testing.T) {
	h := newFlowHarness(t)
	auth := h.authorize("state-code-replay")
	tokens := h.exchangeCodeSuccess(auth.code, flowVerifier(), flowRedirect)

	replay := h.exchangeCode(auth.code, flowVerifier(), flowRedirect)
	assertOAuthError(t, replay, http.StatusBadRequest, "invalid_grant")
	h.assertProtected(h.server, tokens.AccessToken, http.StatusUnauthorized)
	assertOAuthError(t, h.refreshResponse(tokens.RefreshToken), http.StatusBadRequest, "invalid_grant")
	h.assertReplayCommitted("code_replay")
}

func TestOAuthPostgresConcurrentAuthorizationCodeReplay(t *testing.T) {
	h := newFlowHarness(t)
	auth := h.authorize("state-code-race")

	winner := h.raceTokenRequests(func() *httptest.ResponseRecorder {
		return h.exchangeCode(auth.code, flowVerifier(), flowRedirect)
	})
	h.assertProtected(h.server, winner.AccessToken, http.StatusUnauthorized)
	assertOAuthError(t, h.refreshResponse(winner.RefreshToken), http.StatusBadRequest, "invalid_grant")
	h.assertReplayCommitted("code_replay")
}

func TestOAuthPostgresConcurrentRefreshReplay(t *testing.T) {
	h := newFlowHarness(t)
	auth := h.authorize("state-refresh-race")
	tokens := h.exchangeCodeSuccess(auth.code, flowVerifier(), flowRedirect)

	winner := h.raceTokenRequests(func() *httptest.ResponseRecorder {
		return h.refreshResponse(tokens.RefreshToken)
	})
	h.assertProtected(h.server, winner.AccessToken, http.StatusUnauthorized)
	assertOAuthError(t, h.refreshResponse(winner.RefreshToken), http.StatusBadRequest, "invalid_grant")
	h.assertReplayCommitted("refresh_replay")
}

func TestOAuthPostgresConcurrentConsentApprovalAndDenial(t *testing.T) {
	h := newFlowHarness(t)
	pending := h.beginAuthorization("state-decision-race")
	csrf := h.manager.CSRFToken(h.sessionRequest())
	if csrf == "" {
		t.Fatal("CSRFToken() returned an empty token")
	}

	type decisionResult struct {
		decision string
		response *httptest.ResponseRecorder
	}
	start := make(chan struct{})
	results := make(chan decisionResult, 2)
	var wait sync.WaitGroup
	for _, decision := range []string{"approve", "deny"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- decisionResult{decision: decision, response: h.decisionResponse(pending, csrf, decision)}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	committed := 0
	approvalWon := false
	code := ""
	for result := range results {
		location := result.response.Header().Get("Location")
		if location == "" {
			if result.response.Code != http.StatusBadRequest && result.response.Code != http.StatusUnauthorized {
				t.Fatalf("losing %s decision status = %d, want 400 or 401", result.decision, result.response.Code)
			}
			continue
		}
		if result.response.Code != http.StatusSeeOther {
			t.Fatalf("committed %s decision status = %d, want 303", result.decision, result.response.Code)
		}
		redirect, err := url.Parse(location)
		if err != nil {
			t.Fatalf("parse committed decision redirect: %v", err)
		}
		if redirect.Scheme+"://"+redirect.Host+redirect.Path != flowRedirect ||
			redirect.Query().Get("state") != "state-decision-race" || redirect.Query().Get("iss") != flowIssuer {
			t.Fatalf("committed decision redirect = %q", redirect.String())
		}
		committed++
		switch result.decision {
		case "approve":
			code = redirect.Query().Get("code")
			if code == "" || redirect.Query().Get("error") != "" {
				t.Fatalf("approval redirect code = %q, error = %q", code, redirect.Query().Get("error"))
			}
			approvalWon = true
		case "deny":
			if redirect.Query().Get("error") != "access_denied" || redirect.Query().Get("code") != "" {
				t.Fatalf("denial redirect error = %q, code = %q", redirect.Query().Get("error"), redirect.Query().Get("code"))
			}
		}
	}
	if committed != 1 {
		t.Fatalf("committed consent decisions = %d, want 1", committed)
	}

	retry := h.decisionResponse(pending, csrf, "approve")
	if retry.Header().Get("Location") != "" || (retry.Code != http.StatusBadRequest && retry.Code != http.StatusUnauthorized) {
		t.Fatalf("consumed consent retry status = %d, Location = %q", retry.Code, retry.Header().Get("Location"))
	}
	h.assertConsentDecisionCommitted(pending, approvalWon)
	if approvalWon {
		h.exchangeCodeSuccess(code, flowVerifier(), flowRedirect)
	}
}

func newFlowHarness(t *testing.T) *flowHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("PostgreSQL OAuth flow integration test")
	}
	ownerURL := os.Getenv("BANHMI_AUTH_TEST_DATABASE_URL")
	runtimeURL := os.Getenv("BANHMI_AUTH_DATABASE_URL")
	if ownerURL == "" || runtimeURL == "" {
		t.Skip("dedicated owner and runtime authentication database URLs are not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatalf("open owner pool: %v", err)
	}
	t.Cleanup(owner.Close)
	runtime, err := pgxpool.New(ctx, runtimeURL)
	if err != nil {
		t.Fatalf("open runtime pool: %v", err)
	}
	t.Cleanup(runtime.Close)

	origin, err := url.Parse(flowIssuer)
	if err != nil {
		t.Fatalf("parse test origin: %v", err)
	}
	identityStore := identity.NewPostgresStore(runtime)
	manager, err := identity.NewManager(identityStore, identity.Config{
		PublicOrigin: origin, AuthDatabaseURL: runtimeURL,
		GoogleClientID: "flow-client-id", GoogleClientSecret: "flow-client-secret",
		TransactionKey: [32]byte{1, 2, 3, 4},
	})
	if err != nil {
		t.Fatalf("identity.NewManager() error = %v", err)
	}
	registry, err := ParseClientRegistry(flowClientRegistry(flowResource), flowResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	server, err := NewServer(runtime, registry, manager, Config{
		Issuer: flowIssuer, Resource: flowResource,
		GlobalSecret: []byte("0123456789abcdef0123456789abcdef"), ConsentLifetime: 10 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	mux := http.NewServeMux()
	server.Register(mux)

	now := time.Now().UTC().Truncate(time.Microsecond)
	unique := base64.RawURLEncoding.EncodeToString(flowRandom(t, 18))
	rawSession := flowRandom(t, 32)
	sessionHash := sha256.Sum256(rawSession)
	principal, err := identityStore.CompleteLogin(ctx, identity.Profile{
		Issuer: "https://accounts.google.com", Subject: "flow-" + unique,
		Email: "flow-" + unique + "@example.com", DisplayName: "Flow Test",
	}, sessionHash[:], now, now.Add(12*time.Hour))
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	return &flowHarness{
		t: t, ctx: ctx, owner: owner, runtime: runtime, manager: manager, server: server, mux: mux,
		principal: principal,
		session:   &http.Cookie{Name: identity.SessionCookieName, Value: base64.RawURLEncoding.EncodeToString(rawSession)},
	}
}

func (h *flowHarness) authorize(state string) flowAuthorization {
	h.t.Helper()
	pending := h.beginAuthorization(state)
	csrf := h.manager.CSRFToken(h.sessionRequest())
	if csrf == "" {
		h.t.Fatal("CSRFToken() returned an empty token")
	}
	recorder := h.decisionResponse(pending, csrf, "approve")
	if recorder.Code != http.StatusSeeOther {
		h.t.Fatalf("consent decision status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		h.t.Fatalf("parse authorization redirect: %v", err)
	}
	if location.Scheme+"://"+location.Host+location.Path != flowRedirect {
		h.t.Fatalf("authorization redirect = %q, want %q", location.String(), flowRedirect)
	}
	if location.Query().Get("state") != state || location.Query().Get("iss") != flowIssuer {
		h.t.Fatalf("authorization response state = %q, iss = %q", location.Query().Get("state"), location.Query().Get("iss"))
	}
	code := location.Query().Get("code")
	if code == "" {
		h.t.Fatal("authorization response has no code")
	}
	return flowAuthorization{code: code}
}

func (h *flowHarness) decisionResponse(pending *http.Cookie, csrf, decision string) *httptest.ResponseRecorder {
	h.t.Helper()
	form := url.Values{"csrf_token": {csrf}, "decision": {decision}}
	request := httptest.NewRequest(http.MethodPost, flowIssuer+"/oauth/authorize/decision", strings.NewReader(form.Encode())).WithContext(h.ctx)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", flowIssuer)
	request.AddCookie(h.session)
	request.AddCookie(pending)
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func (h *flowHarness) beginAuthorization(state string) *http.Cookie {
	h.t.Helper()
	query := url.Values{
		"response_type": {"code"}, "client_id": {flowClientID}, "redirect_uri": {flowRedirect},
		"scope": {"mcp:read"}, "state": {state}, "resource": {flowResource},
		"code_challenge": {flowChallenge()}, "code_challenge_method": {"S256"},
	}
	request := httptest.NewRequest(http.MethodGet, flowIssuer+"/oauth/authorize?"+query.Encode(), nil).WithContext(h.ctx)
	request.AddCookie(h.session)
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		h.t.Fatalf("authorization status = %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "Flow client") ||
		!strings.Contains(recorder.Body.String(), flowResource) ||
		!strings.Contains(recorder.Body.String(), "mcp:read") {
		h.t.Fatal("consent page omitted approved request fields")
	}
	return flowResponseCookie(h.t, recorder, identity.PendingConsentCookieName)
}

func (h *flowHarness) exchangeCodeSuccess(code, verifier, redirect string) flowTokens {
	h.t.Helper()
	response := h.exchangeCode(code, verifier, redirect)
	return decodeFlowTokens(h.t, response)
}

func (h *flowHarness) exchangeCode(code, verifier, redirect string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.tokenRequest(url.Values{
		"grant_type": {"authorization_code"}, "client_id": {flowClientID},
		"resource": {flowResource}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect},
	})
}

func (h *flowHarness) refresh(token string) flowTokens {
	h.t.Helper()
	return decodeFlowTokens(h.t, h.refreshResponse(token))
}

func (h *flowHarness) refreshResponse(token string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.tokenRequest(url.Values{
		"grant_type": {"refresh_token"}, "client_id": {flowClientID},
		"resource": {flowResource}, "refresh_token": {token},
	})
}

func (h *flowHarness) tokenRequest(form url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	request := httptest.NewRequest(http.MethodPost, flowIssuer+"/oauth/token", strings.NewReader(form.Encode())).WithContext(h.ctx)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	return recorder
}

func (h *flowHarness) raceTokenRequests(send func() *httptest.ResponseRecorder) flowTokens {
	h.t.Helper()
	start := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			responses <- send()
		}()
	}
	close(start)
	wait.Wait()
	close(responses)

	var winner flowTokens
	successes := 0
	rejections := 0
	for response := range responses {
		switch response.Code {
		case http.StatusOK:
			successes++
			winner = decodeFlowTokens(h.t, response)
		case http.StatusBadRequest:
			assertOAuthError(h.t, response, http.StatusBadRequest, "invalid_grant")
			rejections++
		default:
			h.t.Fatalf("concurrent token status = %d, body = %q", response.Code, response.Body.String())
		}
	}
	if successes != 1 || rejections != 1 {
		h.t.Fatalf("concurrent token results: successes = %d, invalid_grant = %d, want 1 each", successes, rejections)
	}
	return winner
}

func (h *flowHarness) assertPersistedAudience() {
	h.t.Helper()
	var clientID string
	var accountID int64
	var accountEpoch int64
	var resource string
	var payload []byte
	err := h.owner.QueryRow(h.ctx, `
		SELECT g.client_id, g.account_id, g.account_epoch, g.resource, s.request_payload
		FROM auth.oauth_grant AS g
		JOIN auth.oauth_session AS s ON s.grant_id = g.id
		WHERE g.account_id = $1 AND s.kind = 'authorize_code'
		ORDER BY g.id DESC
		LIMIT 1`, h.principal.AccountID).Scan(&clientID, &accountID, &accountEpoch, &resource, &payload)
	if err != nil {
		h.t.Fatalf("query persisted OAuth request: %v", err)
	}
	var stored StoredRequest
	if err := json.Unmarshal(payload, &stored); err != nil {
		h.t.Fatalf("decode persisted OAuth request: %v", err)
	}
	if clientID != flowClientID || accountID != h.principal.AccountID || accountEpoch != h.principal.AuthorizationEpoch ||
		resource != flowResource || !exactStringSet(stored.RequestedScopes, "mcp:read") ||
		!exactStringSet(stored.GrantedScopes, "mcp:read") ||
		!exactStringSet(stored.RequestedAudience, flowResource) ||
		!exactStringSet(stored.GrantedAudience, flowResource) || stored.Session.Resource != flowResource {
		h.t.Fatalf("persisted grant client = %q, account = %d/%d, resource = %q, scopes = %q/%q, audience = %q/%q, session resource = %q", clientID, accountID, accountEpoch, resource, stored.RequestedScopes, stored.GrantedScopes, stored.RequestedAudience, stored.GrantedAudience, stored.Session.Resource)
	}
}

func (h *flowHarness) assertBearerBoundary(tokens flowTokens, authorizationCode string) {
	h.t.Helper()
	h.assertProtectedRequest(h.server, "", nil, "access_token="+url.QueryEscape(tokens.AccessToken), http.StatusUnauthorized)
	h.assertProtectedRequest(h.server, "Bearer "+tokens.AccessToken, &http.Cookie{Name: "access_token", Value: tokens.AccessToken}, "", http.StatusUnauthorized)
	h.assertProtectedRequest(h.server, "", h.session, "", http.StatusUnauthorized)
	h.assertProtected(h.server, "legacy-static-key", http.StatusUnauthorized)
	h.assertProtected(h.server, authorizationCode, http.StatusUnauthorized)
	h.assertProtected(h.server, tokens.RefreshToken, http.StatusUnauthorized)
}

func (h *flowHarness) assertRemovedClientRejects(accessToken string) {
	h.t.Helper()
	registry, err := ParseClientRegistry([]byte(`{"version":1,"clients":[]}`), flowResource)
	if err != nil {
		h.t.Fatalf("ParseClientRegistry(empty) error = %v", err)
	}
	server, err := NewServer(h.runtime, registry, h.manager, Config{
		Issuer: flowIssuer, Resource: flowResource,
		GlobalSecret: []byte("0123456789abcdef0123456789abcdef"), ConsentLifetime: 10 * time.Minute,
	})
	if err != nil {
		h.t.Fatalf("NewServer(empty clients) error = %v", err)
	}
	h.assertProtected(server, accessToken, http.StatusUnauthorized)
}

func (h *flowHarness) assertWrongResourceRejects(accessToken string) {
	h.t.Helper()
	issuer := "https://other.example"
	resource := issuer + "/mcp"
	registry, err := ParseClientRegistry(flowClientRegistry(resource), resource)
	if err != nil {
		h.t.Fatalf("ParseClientRegistry(other resource) error = %v", err)
	}
	server, err := NewServer(h.runtime, registry, h.manager, Config{
		Issuer: issuer, Resource: resource,
		GlobalSecret: []byte("0123456789abcdef0123456789abcdef"), ConsentLifetime: 10 * time.Minute,
	})
	if err != nil {
		h.t.Fatalf("NewServer(other resource) error = %v", err)
	}
	h.assertProtected(server, accessToken, http.StatusUnauthorized)
}

func (h *flowHarness) assertProtected(server *Server, token string, want int) {
	h.t.Helper()
	h.assertProtectedRequest(server, "Bearer "+token, nil, "", want)
}

func (h *flowHarness) assertProtectedRequest(server *Server, authorization string, cookie *http.Cookie, rawQuery string, want int) {
	h.t.Helper()
	called := false
	handler := server.Protect(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	target := flowIssuer + "/mcp"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	request := httptest.NewRequest(http.MethodPost, target, nil).WithContext(h.ctx)
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != want || called != (want == http.StatusNoContent) {
		h.t.Fatalf("protected request status = %d, called = %t, want status %d", recorder.Code, called, want)
	}
}

func (h *flowHarness) assertReplayCommitted(kind string) {
	h.t.Helper()
	var revoked bool
	var reason string
	err := h.owner.QueryRow(h.ctx, `
		SELECT revoked_at IS NOT NULL, COALESCE(revocation_reason, '')
		FROM auth.oauth_grant
		WHERE account_id = $1
		ORDER BY id DESC
		LIMIT 1`, h.principal.AccountID).Scan(&revoked, &reason)
	if err != nil {
		h.t.Fatalf("query replayed grant: %v", err)
	}
	if !revoked || reason == "" {
		h.t.Fatalf("replayed grant revoked = %t, reason = %q", revoked, reason)
	}
	var events int
	if err := h.owner.QueryRow(h.ctx, `
		SELECT count(*)
		FROM auth.oauth_security_event
		WHERE account_id = $1 AND event_kind = $2`, h.principal.AccountID, kind).Scan(&events); err != nil {
		h.t.Fatalf("count replay audit events: %v", err)
	}
	if events != 1 {
		h.t.Fatalf("%s audit events = %d, want 1", kind, events)
	}
}

func (h *flowHarness) assertConsentDecisionCommitted(pending *http.Cookie, approvalWon bool) {
	h.t.Helper()
	handle, err := base64.RawURLEncoding.DecodeString(pending.Value)
	if err != nil {
		h.t.Fatalf("decode pending-consent cookie: %v", err)
	}
	handleHash := hashOAuthHandle(handle)
	var rows int
	var consumed int
	if err := h.owner.QueryRow(h.ctx, `
		SELECT count(*), count(consumed_at)
		FROM auth.pending_consent
		WHERE handle_hash = $1`, handleHash).Scan(&rows, &consumed); err != nil {
		h.t.Fatalf("query raced pending consent: %v", err)
	}
	if rows != 1 || consumed != 1 {
		h.t.Fatalf("pending-consent rows = %d, consumed rows = %d, want 1 each", rows, consumed)
	}

	var grants int
	var codes int
	if err := h.owner.QueryRow(h.ctx, `
		SELECT count(DISTINCT g.id), count(s.id) FILTER (WHERE s.kind = 'authorize_code')
		FROM auth.oauth_grant AS g
		LEFT JOIN auth.oauth_session AS s ON s.grant_id = g.id
		WHERE g.account_id = $1`, h.principal.AccountID).Scan(&grants, &codes); err != nil {
		h.t.Fatalf("query raced consent grant: %v", err)
	}
	want := 0
	if approvalWon {
		want = 1
	}
	if grants != want || codes != want {
		h.t.Fatalf("consent race grants = %d, authorization codes = %d, want %d each", grants, codes, want)
	}
}

func (h *flowHarness) assertPendingResume(pending *http.Cookie, want int) {
	h.t.Helper()
	request := httptest.NewRequest(http.MethodGet, flowIssuer+"/oauth/resume", nil).WithContext(h.ctx)
	request.AddCookie(h.session)
	request.AddCookie(pending)
	recorder := httptest.NewRecorder()
	h.mux.ServeHTTP(recorder, request)
	if recorder.Code != want {
		h.t.Fatalf("pending-consent resume status = %d, want %d", recorder.Code, want)
	}
}

func (h *flowHarness) sessionRequest() *http.Request {
	request := httptest.NewRequest(http.MethodGet, flowIssuer+"/app", nil).WithContext(h.ctx)
	request.AddCookie(h.session)
	return request
}

func decodeFlowTokens(t *testing.T, recorder *httptest.ResponseRecorder) flowTokens {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("token status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	var tokens flowTokens
	if err := json.Unmarshal(recorder.Body.Bytes(), &tokens); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || !strings.EqualFold(tokens.TokenType, "bearer") {
		t.Fatalf("token response omitted credentials or bearer type: %#v", tokens)
	}
	return tokens
}

func assertOAuthError(t *testing.T, recorder *httptest.ResponseRecorder, status int, name string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("OAuth error status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode OAuth error: %v", err)
	}
	if response.Error != name {
		t.Fatalf("OAuth error = %q, want %q", response.Error, name)
	}
}

func flowResponseCookie(t *testing.T, recorder *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, cookie := range recorder.Result().Cookies() {
		if cookie.Name == name {
			return cookie
		}
	}
	t.Fatalf("response has no %s cookie", name)
	return nil
}

func flowVerifier() string {
	return "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~"
}

func flowChallenge() string {
	sum := sha256.Sum256([]byte(flowVerifier()))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func flowRandom(t *testing.T, size int) []byte {
	t.Helper()
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("rand.Read() error = %v", err)
	}
	return value
}

func flowClientRegistry(resource string) []byte {
	return []byte(fmt.Sprintf(`{
		"version": 1,
		"clients": [{
			"client_id": %q,
			"client_name": "Flow client",
			"redirect_uris": [%q],
			"token_endpoint_auth_method": "none",
			"grant_types": ["authorization_code", "refresh_token"],
			"response_types": ["code"],
			"scope": "mcp:read"
		}]
	}`, flowClientID, flowRedirect))
}
