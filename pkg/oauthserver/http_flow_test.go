package oauthserver

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"

	"danny.vn/banhmi/pkg/identity"
)

type fakeProtocolOperation struct {
	ctx    context.Context
	finish func(error) (FinishOutcome, error)
}

func (o *fakeProtocolOperation) Context() context.Context                { return o.ctx }
func (o *fakeProtocolOperation) Finish(err error) (FinishOutcome, error) { return o.finish(err) }
func (o *fakeProtocolOperation) Rollback() error                         { return nil }

type responseTrackingWriter struct {
	target    http.ResponseWriter
	committed bool
}

func (w *responseTrackingWriter) Header() http.Header { return w.target.Header() }
func (w *responseTrackingWriter) WriteHeader(status int) {
	w.committed = true
	w.target.WriteHeader(status)
}
func (w *responseTrackingWriter) Write(body []byte) (int, error) {
	w.committed = true
	return w.target.Write(body)
}

type fakeProtocolProvider struct {
	newAuthorizeRequest     func(context.Context, *http.Request) (fosite.AuthorizeRequester, error)
	newAuthorizeResponse    func(context.Context, fosite.AuthorizeRequester, fosite.Session) (fosite.AuthorizeResponder, error)
	writeAuthorizeResponse  func(context.Context, http.ResponseWriter, fosite.AuthorizeRequester, fosite.AuthorizeResponder)
	newAccessRequest        func(context.Context, *http.Request, fosite.Session) (fosite.AccessRequester, error)
	newAccessResponse       func(context.Context, fosite.AccessRequester) (fosite.AccessResponder, error)
	writeAccessError        func(context.Context, http.ResponseWriter, fosite.AccessRequester, error)
	writeAccessResponse     func(context.Context, http.ResponseWriter, fosite.AccessRequester, fosite.AccessResponder)
	newRevocationRequest    func(context.Context, *http.Request) error
	writeRevocationResponse func(context.Context, http.ResponseWriter, error)
	introspectToken         func(context.Context, string, fosite.TokenUse, fosite.Session, ...string) (fosite.TokenUse, fosite.AccessRequester, error)
}

func (p *fakeProtocolProvider) NewAuthorizeRequest(ctx context.Context, r *http.Request) (fosite.AuthorizeRequester, error) {
	return p.newAuthorizeRequest(ctx, r)
}
func (p *fakeProtocolProvider) NewAuthorizeResponse(ctx context.Context, r fosite.AuthorizeRequester, session fosite.Session) (fosite.AuthorizeResponder, error) {
	return p.newAuthorizeResponse(ctx, r, session)
}
func (p *fakeProtocolProvider) WriteAuthorizeResponse(ctx context.Context, w http.ResponseWriter, r fosite.AuthorizeRequester, response fosite.AuthorizeResponder) {
	p.writeAuthorizeResponse(ctx, w, r, response)
}
func (p *fakeProtocolProvider) NewAccessRequest(ctx context.Context, r *http.Request, session fosite.Session) (fosite.AccessRequester, error) {
	return p.newAccessRequest(ctx, r, session)
}
func (p *fakeProtocolProvider) NewAccessResponse(ctx context.Context, r fosite.AccessRequester) (fosite.AccessResponder, error) {
	return p.newAccessResponse(ctx, r)
}
func (p *fakeProtocolProvider) WriteAccessError(ctx context.Context, w http.ResponseWriter, r fosite.AccessRequester, err error) {
	p.writeAccessError(ctx, w, r, err)
}
func (p *fakeProtocolProvider) WriteAccessResponse(ctx context.Context, w http.ResponseWriter, r fosite.AccessRequester, response fosite.AccessResponder) {
	p.writeAccessResponse(ctx, w, r, response)
}
func (p *fakeProtocolProvider) NewRevocationRequest(ctx context.Context, r *http.Request) error {
	return p.newRevocationRequest(ctx, r)
}
func (p *fakeProtocolProvider) WriteRevocationResponse(ctx context.Context, w http.ResponseWriter, err error) {
	p.writeRevocationResponse(ctx, w, err)
}
func (p *fakeProtocolProvider) IntrospectToken(ctx context.Context, token string, use fosite.TokenUse, session fosite.Session, scopes ...string) (fosite.TokenUse, fosite.AccessRequester, error) {
	return p.introspectToken(ctx, token, use, session, scopes...)
}

type fakeWebsiteSessions struct {
	principal identity.Principal
}

func (s *fakeWebsiteSessions) Authenticate(*http.Request) (identity.Principal, error) {
	return s.principal, nil
}
func (s *fakeWebsiteSessions) CSRFToken(*http.Request) string                                { return "csrf" }
func (s *fakeWebsiteSessions) ValidCSRF(_ *http.Request, token string) bool                  { return token == "csrf" }
func (s *fakeWebsiteSessions) BeginLogin(http.ResponseWriter, *http.Request, string, []byte) {}
func (s *fakeWebsiteSessions) Seal(_ string, plain []byte) ([]byte, error) {
	return append([]byte(nil), plain...), nil
}
func (s *fakeWebsiteSessions) Open(_ string, sealed []byte) ([]byte, error) {
	return append([]byte(nil), sealed...), nil
}

type testAuthorizeResponder struct {
	code       string
	header     http.Header
	parameters url.Values
}

func (r *testAuthorizeResponder) GetCode() string                { return r.code }
func (r *testAuthorizeResponder) GetHeader() http.Header         { return r.header }
func (r *testAuthorizeResponder) AddHeader(key, value string)    { r.header.Add(key, value) }
func (r *testAuthorizeResponder) GetParameters() url.Values      { return r.parameters }
func (r *testAuthorizeResponder) AddParameter(key, value string) { r.parameters.Add(key, value) }

type testAccessResponder struct {
	accessToken string
	tokenType   string
	extra       map[string]any
}

func (r *testAccessResponder) SetExtra(key string, value any)   { r.extra[key] = value }
func (r *testAccessResponder) GetExtra(key string) any          { return r.extra[key] }
func (r *testAccessResponder) SetExpiresIn(value time.Duration) { r.extra["expires_in"] = value }
func (r *testAccessResponder) SetScopes(value fosite.Arguments) { r.extra["scope"] = value }
func (r *testAccessResponder) SetAccessToken(value string)      { r.accessToken = value }
func (r *testAccessResponder) SetTokenType(value string)        { r.tokenType = value }
func (r *testAccessResponder) GetAccessToken() string           { return r.accessToken }
func (r *testAccessResponder) GetTokenType() string             { return r.tokenType }
func (r *testAccessResponder) ToMap() map[string]any            { return r.extra }

func TestTokenAndRefreshWriteOnlyAfterFinish(t *testing.T) {
	for _, test := range []struct {
		name      string
		body      string
		kind      CredentialKind
		presented string
	}{
		{name: "code", body: "grant_type=authorization_code&client_id=client&resource=" + url.QueryEscape(testResource) + "&code=code&code_verifier=verifier&redirect_uri=" + url.QueryEscape("https://client.example/callback"), kind: CredentialCode, presented: "code"},
		{name: "refresh without resource", body: "grant_type=refresh_token&client_id=client&refresh_token=refresh", kind: CredentialRefresh, presented: "refresh"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writer := &responseTrackingWriter{target: recorder}
			server := &Server{cfg: Config{Resource: testResource}}
			server.beginOperation = func(ctx context.Context, _ Binding, kind CredentialKind, presented string) (protocolOperation, error) {
				if kind != test.kind || presented != test.presented {
					t.Fatalf("credential = %v, %q", kind, presented)
				}
				return &fakeProtocolOperation{ctx: ctx, finish: func(err error) (FinishOutcome, error) {
					if err != nil || writer.committed {
						t.Fatalf("Finish input = %v, committed = %v", err, writer.committed)
					}
					return OutcomeCommitted, nil
				}}, nil
			}
			accessRequest := fosite.NewAccessRequest(NewSession(0, 0, testResource))
			server.provider = &fakeProtocolProvider{
				newAccessRequest: func(_ context.Context, r *http.Request, _ fosite.Session) (fosite.AccessRequester, error) {
					if r.PostForm.Get("audience") != testResource {
						t.Fatalf("audience = %q", r.PostForm.Get("audience"))
					}
					return accessRequest, nil
				},
				newAccessResponse: func(context.Context, fosite.AccessRequester) (fosite.AccessResponder, error) {
					return &testAccessResponder{extra: map[string]any{}}, nil
				},
				writeAccessResponse: func(_ context.Context, w http.ResponseWriter, _ fosite.AccessRequester, _ fosite.AccessResponder) {
					w.WriteHeader(http.StatusOK)
				},
				writeAccessError: func(context.Context, http.ResponseWriter, fosite.AccessRequester, error) {
					t.Fatal("WriteAccessError called")
				},
			}
			request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/token", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			server.handleToken(writer, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d", recorder.Code)
			}
		})
	}
}

func TestRevocationStorageFailureReturns503WithoutSDKWrite(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &responseTrackingWriter{target: recorder}
	server := &Server{cfg: Config{Resource: testResource}}
	server.beginOperation = func(ctx context.Context, binding Binding, _ CredentialKind, _ string) (protocolOperation, error) {
		if binding.Resource != testResource {
			t.Fatalf("binding resource = %q", binding.Resource)
		}
		return &fakeProtocolOperation{ctx: ctx, finish: func(error) (FinishOutcome, error) {
			if writer.committed {
				t.Fatal("response committed before Finish")
			}
			return OutcomeRolledBack, errors.New("storage failed")
		}}, nil
	}
	server.provider = &fakeProtocolProvider{
		newRevocationRequest:    func(context.Context, *http.Request) error { return nil },
		writeRevocationResponse: func(context.Context, http.ResponseWriter, error) { t.Fatal("WriteRevocationResponse called") },
	}
	body := "client_id=client&token=credential"
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/revoke", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.handleRevoke(writer, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestRevocationProtocolStorageErrorReturns503WithoutSDKWrite(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &responseTrackingWriter{target: recorder}
	server := &Server{cfg: Config{Resource: testResource}}
	server.beginOperation = func(ctx context.Context, _ Binding, _ CredentialKind, _ string) (protocolOperation, error) {
		return &fakeProtocolOperation{ctx: ctx, finish: func(protocolErr error) (FinishOutcome, error) {
			if !errors.Is(protocolErr, fosite.ErrTemporarilyUnavailable) || writer.committed {
				t.Fatalf("Finish input = %v, committed = %v", protocolErr, writer.committed)
			}
			return OutcomeRolledBack, nil
		}}, nil
	}
	server.provider = &fakeProtocolProvider{
		newRevocationRequest:    func(context.Context, *http.Request) error { return fosite.ErrTemporarilyUnavailable },
		writeRevocationResponse: func(context.Context, http.ResponseWriter, error) { t.Fatal("WriteRevocationResponse called") },
	}
	body := "client_id=client&token=credential"
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/revoke", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.handleRevoke(writer, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestTokenFinishFailureDoesNotWriteCredentialOrSDKError(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &responseTrackingWriter{target: recorder}
	server := &Server{cfg: Config{Resource: testResource}}
	server.beginOperation = func(ctx context.Context, _ Binding, _ CredentialKind, _ string) (protocolOperation, error) {
		return &fakeProtocolOperation{ctx: ctx, finish: func(error) (FinishOutcome, error) {
			if writer.committed || recorder.Header().Get("Location") != "" {
				t.Fatal("response written before Finish")
			}
			return OutcomeRolledBack, errors.New("commit failed")
		}}, nil
	}
	server.provider = &fakeProtocolProvider{
		newAccessRequest: func(context.Context, *http.Request, fosite.Session) (fosite.AccessRequester, error) {
			return fosite.NewAccessRequest(NewSession(0, 0, testResource)), nil
		},
		newAccessResponse: func(context.Context, fosite.AccessRequester) (fosite.AccessResponder, error) {
			return &testAccessResponder{accessToken: "secret-token", extra: map[string]any{}}, nil
		},
		writeAccessError: func(context.Context, http.ResponseWriter, fosite.AccessRequester, error) {
			t.Fatal("WriteAccessError called")
		},
		writeAccessResponse: func(context.Context, http.ResponseWriter, fosite.AccessRequester, fosite.AccessResponder) {
			t.Fatal("WriteAccessResponse called")
		},
	}
	body := "grant_type=refresh_token&client_id=client&resource=" + url.QueryEscape(testResource) + "&refresh_token=refresh"
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/token", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.handleToken(writer, request)
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "secret-token") {
		t.Fatalf("status = %d, body = %q", recorder.Code, recorder.Body.String())
	}
}

func TestReplayErrorIsWrittenOnlyAfterFinish(t *testing.T) {
	recorder := httptest.NewRecorder()
	writer := &responseTrackingWriter{target: recorder}
	finished := false
	server := &Server{cfg: Config{Resource: testResource}}
	server.beginOperation = func(ctx context.Context, _ Binding, _ CredentialKind, _ string) (protocolOperation, error) {
		return &fakeProtocolOperation{ctx: ctx, finish: func(err error) (FinishOutcome, error) {
			if !errors.Is(err, fosite.ErrInvalidGrant) || writer.committed {
				t.Fatalf("Finish input = %v, committed = %v", err, writer.committed)
			}
			finished = true
			return OutcomeReplayCommitted, nil
		}}, nil
	}
	server.provider = &fakeProtocolProvider{
		newAccessRequest: func(context.Context, *http.Request, fosite.Session) (fosite.AccessRequester, error) {
			return fosite.NewAccessRequest(NewSession(0, 0, testResource)), fosite.ErrInvalidGrant
		},
		writeAccessError: func(_ context.Context, w http.ResponseWriter, _ fosite.AccessRequester, _ error) {
			if !finished {
				t.Fatal("WriteAccessError called before Finish")
			}
			oauthJSONError(w, http.StatusBadRequest, "invalid_grant")
		},
		writeAccessResponse: func(context.Context, http.ResponseWriter, fosite.AccessRequester, fosite.AccessResponder) {
			t.Fatal("WriteAccessResponse called")
		},
	}
	body := "grant_type=refresh_token&client_id=client&resource=" + url.QueryEscape(testResource) + "&refresh_token=refresh"
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/token", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	server.handleToken(writer, request)
	if !finished || recorder.Code != http.StatusBadRequest {
		t.Fatalf("finished = %v, status = %d", finished, recorder.Code)
	}
}

func TestBearerAdmissionFinishesBeforeHandler(t *testing.T) {
	registry := httpTestRegistry(t)
	client, err := registry.GetClient(t.Context(), "client")
	if err != nil {
		t.Fatal(err)
	}
	accessRequest := fosite.NewAccessRequest(NewSession(42, 3, testResource))
	accessRequest.Client = client
	accessRequest.GrantScope("mcp:read")
	accessRequest.GrantAudience(testResource)
	called := false
	server := &Server{clients: registry, cfg: Config{Resource: testResource}}
	server.beginOperation = func(ctx context.Context, _ Binding, _ CredentialKind, _ string) (protocolOperation, error) {
		return &fakeProtocolOperation{ctx: ctx, finish: func(err error) (FinishOutcome, error) {
			if err != nil || called {
				t.Fatalf("Finish input = %v, called = %v", err, called)
			}
			return OutcomeCommitted, nil
		}}, nil
	}
	server.provider = &fakeProtocolProvider{introspectToken: func(context.Context, string, fosite.TokenUse, fosite.Session, ...string) (fosite.TokenUse, fosite.AccessRequester, error) {
		return fosite.AccessToken, accessRequest, nil
	}}
	handler := server.Protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/mcp", nil)
	request.Header.Set("Authorization", "Bearer credential")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !called {
		t.Fatal("protected handler was not called")
	}
}

func TestBearerFinishFailureReturns503BeforeHandler(t *testing.T) {
	registry := httpTestRegistry(t)
	client, err := registry.GetClient(t.Context(), "client")
	if err != nil {
		t.Fatal(err)
	}
	accessRequest := fosite.NewAccessRequest(NewSession(42, 3, testResource))
	accessRequest.Client = client
	accessRequest.GrantScope("mcp:read")
	accessRequest.GrantAudience(testResource)
	called := false
	recorder := httptest.NewRecorder()
	writer := &responseTrackingWriter{target: recorder}
	server := &Server{clients: registry, cfg: Config{Resource: testResource}}
	server.beginOperation = func(ctx context.Context, _ Binding, _ CredentialKind, _ string) (protocolOperation, error) {
		return &fakeProtocolOperation{ctx: ctx, finish: func(error) (FinishOutcome, error) {
			if writer.committed || called {
				t.Fatal("request advanced before Finish")
			}
			return OutcomeRolledBack, errors.New("commit failed")
		}}, nil
	}
	server.provider = &fakeProtocolProvider{introspectToken: func(context.Context, string, fosite.TokenUse, fosite.Session, ...string) (fosite.TokenUse, fosite.AccessRequester, error) {
		return fosite.AccessToken, accessRequest, nil
	}}
	handler := server.Protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/mcp", nil)
	request.Header.Set("Authorization", "Bearer credential")
	handler.ServeHTTP(writer, request)
	if called || recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("called = %v, status = %d", called, recorder.Code)
	}
}

func TestConsentDecisionCommitsBeforeRedirect(t *testing.T) {
	for _, test := range []struct {
		name          string
		decision      string
		storeDecision ConsentDecision
		outcome       FinishOutcome
	}{
		{name: "approve", decision: "approve", storeDecision: ConsentApprove, outcome: OutcomeCommitted},
		{name: "deny", decision: "deny", storeDecision: ConsentDeny, outcome: OutcomeDeniedCommitted},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := httpTestRegistry(t)
			payload, requester := pendingTestAuthorization(t, registry)
			recorder := httptest.NewRecorder()
			writer := &responseTrackingWriter{target: recorder}
			finished := false
			server := &Server{
				clients:  registry,
				sessions: &fakeWebsiteSessions{principal: identity.Principal{AccountID: 42, AuthorizationEpoch: 3, SessionHash: []byte("session")}},
				cfg:      Config{Issuer: "https://evidence.example", Resource: testResource},
				now:      func() time.Time { return time.Unix(100, 0) },
			}
			server.readPendingConsent = func(context.Context, []byte, []byte, time.Time) (PendingConsent, error) {
				return PendingConsent{RequestCiphertext: payload}, nil
			}
			server.consumePendingConsent = func(_ context.Context, _, _ []byte, _ time.Time, decision ConsentDecision) (PendingConsent, error) {
				if decision != test.storeDecision {
					t.Fatalf("decision = %v", decision)
				}
				return PendingConsent{RequestCiphertext: payload}, nil
			}
			server.beginOperation = func(ctx context.Context, _ Binding, _ CredentialKind, _ string) (protocolOperation, error) {
				return &fakeProtocolOperation{ctx: ctx, finish: func(protocolErr error) (FinishOutcome, error) {
					if writer.committed || recorder.Header().Get("Location") != "" {
						t.Fatal("response written before Finish")
					}
					if test.storeDecision == ConsentDeny && !errors.Is(protocolErr, fosite.ErrAccessDenied) {
						t.Fatalf("Finish error = %v", protocolErr)
					}
					if test.storeDecision == ConsentApprove && protocolErr != nil {
						t.Fatalf("Finish error = %v", protocolErr)
					}
					finished = true
					return test.outcome, nil
				}}, nil
			}
			server.provider = &fakeProtocolProvider{
				newAuthorizeRequest: func(context.Context, *http.Request) (fosite.AuthorizeRequester, error) { return requester, nil },
				newAuthorizeResponse: func(context.Context, fosite.AuthorizeRequester, fosite.Session) (fosite.AuthorizeResponder, error) {
					return &testAuthorizeResponder{code: "code", header: http.Header{}, parameters: url.Values{}}, nil
				},
				writeAuthorizeResponse: func(_ context.Context, w http.ResponseWriter, _ fosite.AuthorizeRequester, _ fosite.AuthorizeResponder) {
					if !finished {
						t.Fatal("authorize response written before Finish")
					}
					w.WriteHeader(http.StatusSeeOther)
				},
			}
			body := "csrf_token=csrf&decision=" + test.decision
			request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/authorize/decision", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Origin", "https://evidence.example")
			request.AddCookie(&http.Cookie{Name: identity.PendingConsentCookieName, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
			server.handleDecision(writer, request)
			if !finished || recorder.Code != http.StatusSeeOther {
				t.Fatalf("finished = %v, status = %d", finished, recorder.Code)
			}
		})
	}
}

func TestConsentApprovalFinishFailureDoesNotRedirectOrWriteCode(t *testing.T) {
	registry := httpTestRegistry(t)
	payload, requester := pendingTestAuthorization(t, registry)
	recorder := httptest.NewRecorder()
	writer := &responseTrackingWriter{target: recorder}
	responder := &testAuthorizeResponder{code: "secret-code", header: http.Header{}, parameters: url.Values{}}
	server := &Server{
		clients:  registry,
		sessions: &fakeWebsiteSessions{principal: identity.Principal{AccountID: 42, AuthorizationEpoch: 3, SessionHash: []byte("session")}},
		cfg:      Config{Issuer: "https://evidence.example", Resource: testResource},
		now:      func() time.Time { return time.Unix(100, 0) },
	}
	server.readPendingConsent = func(context.Context, []byte, []byte, time.Time) (PendingConsent, error) {
		return PendingConsent{RequestCiphertext: payload}, nil
	}
	server.consumePendingConsent = func(context.Context, []byte, []byte, time.Time, ConsentDecision) (PendingConsent, error) {
		return PendingConsent{RequestCiphertext: payload}, nil
	}
	server.beginOperation = func(ctx context.Context, _ Binding, _ CredentialKind, _ string) (protocolOperation, error) {
		return &fakeProtocolOperation{ctx: ctx, finish: func(error) (FinishOutcome, error) {
			if writer.committed || recorder.Header().Get("Location") != "" || len(recorder.Header().Values("Set-Cookie")) != 0 || responder.parameters.Has("iss") {
				t.Fatal("authorization output changed before Finish")
			}
			return OutcomeRolledBack, errors.New("commit failed")
		}}, nil
	}
	server.provider = &fakeProtocolProvider{
		newAuthorizeRequest: func(context.Context, *http.Request) (fosite.AuthorizeRequester, error) { return requester, nil },
		newAuthorizeResponse: func(context.Context, fosite.AuthorizeRequester, fosite.Session) (fosite.AuthorizeResponder, error) {
			return responder, nil
		},
		writeAuthorizeResponse: func(context.Context, http.ResponseWriter, fosite.AuthorizeRequester, fosite.AuthorizeResponder) {
			t.Fatal("WriteAuthorizeResponse called")
		},
	}
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/authorize/decision", strings.NewReader("csrf_token=csrf&decision=approve"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://evidence.example")
	request.AddCookie(&http.Cookie{Name: identity.PendingConsentCookieName, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	server.handleDecision(writer, request)
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "secret-code") || responder.parameters.Has("iss") {
		t.Fatalf("status = %d, body = %q, parameters = %v", recorder.Code, recorder.Body.String(), responder.parameters)
	}
}

func TestConsentConsumeStorageFailureReturns503AfterFinish(t *testing.T) {
	registry := httpTestRegistry(t)
	payload, requester := pendingTestAuthorization(t, registry)
	recorder := httptest.NewRecorder()
	writer := &responseTrackingWriter{target: recorder}
	finished := false
	server := &Server{
		clients:  registry,
		sessions: &fakeWebsiteSessions{principal: identity.Principal{AccountID: 42, AuthorizationEpoch: 3, SessionHash: []byte("session")}},
		cfg:      Config{Issuer: "https://evidence.example", Resource: testResource},
		now:      func() time.Time { return time.Unix(100, 0) },
	}
	server.readPendingConsent = func(context.Context, []byte, []byte, time.Time) (PendingConsent, error) {
		return PendingConsent{RequestCiphertext: payload}, nil
	}
	server.consumePendingConsent = func(context.Context, []byte, []byte, time.Time, ConsentDecision) (PendingConsent, error) {
		return PendingConsent{}, errStorageFailure
	}
	server.beginOperation = func(ctx context.Context, _ Binding, _ CredentialKind, _ string) (protocolOperation, error) {
		return &fakeProtocolOperation{ctx: ctx, finish: func(protocolErr error) (FinishOutcome, error) {
			if !errors.Is(protocolErr, errStorageFailure) || writer.committed {
				t.Fatalf("Finish input = %v, committed = %v", protocolErr, writer.committed)
			}
			finished = true
			return OutcomeRolledBack, nil
		}}, nil
	}
	server.provider = &fakeProtocolProvider{
		newAuthorizeRequest: func(context.Context, *http.Request) (fosite.AuthorizeRequester, error) { return requester, nil },
		writeAuthorizeResponse: func(context.Context, http.ResponseWriter, fosite.AuthorizeRequester, fosite.AuthorizeResponder) {
			t.Fatal("WriteAuthorizeResponse called")
		},
	}
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/authorize/decision", strings.NewReader("csrf_token=csrf&decision=approve"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://evidence.example")
	request.AddCookie(&http.Cookie{Name: identity.PendingConsentCookieName, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	server.handleDecision(writer, request)
	if !finished || recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Location") != "" {
		t.Fatalf("finished = %v, status = %d, Location = %q", finished, recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestResumeBindsAndRendersPendingConsent(t *testing.T) {
	registry := httpTestRegistry(t)
	payload, requester := pendingTestAuthorization(t, registry)
	bound := false
	server := &Server{
		clients:  registry,
		sessions: &fakeWebsiteSessions{principal: identity.Principal{AccountID: 42, AuthorizationEpoch: 3, SessionHash: []byte("session")}},
		provider: &fakeProtocolProvider{newAuthorizeRequest: func(context.Context, *http.Request) (fosite.AuthorizeRequester, error) { return requester, nil }},
		cfg:      Config{Issuer: "https://evidence.example", Resource: testResource},
		now:      func() time.Time { return time.Unix(100, 0) },
	}
	server.bindPendingConsent = func(context.Context, []byte, []byte, time.Time) (PendingConsent, error) {
		bound = true
		return PendingConsent{RequestCiphertext: payload}, nil
	}
	request := httptest.NewRequest(http.MethodGet, "https://evidence.example/oauth/resume", nil)
	request.AddCookie(&http.Cookie{Name: identity.PendingConsentCookieName, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	recorder := httptest.NewRecorder()
	server.handleResume(recorder, request)
	if !bound || recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "Client name") {
		t.Fatalf("bound = %v, status = %d, body = %q", bound, recorder.Code, recorder.Body.String())
	}
}

func TestResumeStorageFailureReturns503(t *testing.T) {
	server := &Server{
		sessions: &fakeWebsiteSessions{principal: identity.Principal{AccountID: 42, AuthorizationEpoch: 3, SessionHash: []byte("session")}},
		now:      func() time.Time { return time.Unix(100, 0) },
	}
	server.bindPendingConsent = func(context.Context, []byte, []byte, time.Time) (PendingConsent, error) {
		return PendingConsent{}, errStorageFailure
	}
	request := httptest.NewRequest(http.MethodGet, "https://evidence.example/oauth/resume", nil)
	request.AddCookie(&http.Cookie{Name: identity.PendingConsentCookieName, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	recorder := httptest.NewRecorder()
	server.handleResume(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestDecisionPreReadStorageFailureReturns503(t *testing.T) {
	server := &Server{
		sessions: &fakeWebsiteSessions{principal: identity.Principal{AccountID: 42, AuthorizationEpoch: 3, SessionHash: []byte("session")}},
		cfg:      Config{Issuer: "https://evidence.example"},
		now:      func() time.Time { return time.Unix(100, 0) },
	}
	server.readPendingConsent = func(context.Context, []byte, []byte, time.Time) (PendingConsent, error) {
		return PendingConsent{}, errStorageFailure
	}
	request := httptest.NewRequest(http.MethodPost, "https://evidence.example/oauth/authorize/decision", strings.NewReader("csrf_token=csrf&decision=approve"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://evidence.example")
	request.AddCookie(&http.Cookie{Name: identity.PendingConsentCookieName, Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	recorder := httptest.NewRecorder()
	server.handleDecision(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func pendingTestAuthorization(t *testing.T, registry *ClientRegistry) ([]byte, fosite.AuthorizeRequester) {
	t.Helper()
	values := url.Values{
		"client_id": {"client"}, "redirect_uri": {"https://client.example/callback"},
		"response_type": {"code"}, "scope": {"mcp:read"}, "resource": {testResource}, "state": {"state-value"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(make([]byte, 32))}, "code_challenge_method": {"S256"},
	}
	payload, err := encodePendingAuthorization(values)
	if err != nil {
		t.Fatal(err)
	}
	client, err := registry.GetClient(t.Context(), "client")
	if err != nil {
		t.Fatal(err)
	}
	redirect, err := url.Parse(values.Get("redirect_uri"))
	if err != nil {
		t.Fatal(err)
	}
	requester := fosite.NewAuthorizeRequest()
	requester.Client = client
	requester.RedirectURI = redirect
	requester.State = values.Get("state")
	requester.ResponseTypes = fosite.Arguments{"code"}
	requester.Form = cloneForm(values)
	requester.SetRequestedScopes(fosite.Arguments{"mcp:read"})
	requester.SetRequestedAudience(fosite.Arguments{testResource})
	return payload, requester
}

func httpTestRegistry(t *testing.T) *ClientRegistry {
	t.Helper()
	registry, err := ParseClientRegistry([]byte(clientRegistryJSON(clientJSON("client", "https://client.example/callback"))), testResource)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
