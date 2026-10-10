package oauthserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"

	"danny.vn/banhmi/pkg/identity"
)

const oauthFormBodyLimit = 16 << 10

// WebsiteSessions supplies the authenticated browser session and encryption
// operations needed to complete explicit OAuth consent.
type WebsiteSessions interface {
	Authenticate(*http.Request) (identity.Principal, error)
	CSRFToken(*http.Request) string
	ValidCSRF(*http.Request, string) bool
	BeginLogin(http.ResponseWriter, *http.Request, string, []byte)
	Seal(string, []byte) ([]byte, error)
	Open(string, []byte) ([]byte, error)
}

// Server serves OAuth metadata, protocol endpoints, consent, and bearer admission.
type Server struct {
	clients *ClientRegistry
	sessions WebsiteSessions
	provider protocolProvider
	cfg Config
	now func() time.Time
	beginOperation func(context.Context, Binding, CredentialKind, string) (protocolOperation, error)
	createPendingConsent func(context.Context, []byte, []byte, time.Time) error
	bindPendingConsent func(context.Context, []byte, []byte, time.Time) (PendingConsent, error)
	readPendingConsent func(context.Context, []byte, []byte, time.Time) (PendingConsent, error)
	consumePendingConsent func(context.Context, []byte, []byte, time.Time, ConsentDecision) (PendingConsent, error)
}

type protocolOperation interface {
	Context() context.Context
	Finish(error) (FinishOutcome, error)
	Rollback() error
}

type protocolProvider interface {
	NewAuthorizeRequest(context.Context, *http.Request) (fosite.AuthorizeRequester, error)
	NewAuthorizeResponse(context.Context, fosite.AuthorizeRequester, fosite.Session) (fosite.AuthorizeResponder, error)
	WriteAuthorizeResponse(context.Context, http.ResponseWriter, fosite.AuthorizeRequester, fosite.AuthorizeResponder)
	NewAccessRequest(context.Context, *http.Request, fosite.Session) (fosite.AccessRequester, error)
	NewAccessResponse(context.Context, fosite.AccessRequester) (fosite.AccessResponder, error)
	WriteAccessError(context.Context, http.ResponseWriter, fosite.AccessRequester, error)
	WriteAccessResponse(context.Context, http.ResponseWriter, fosite.AccessRequester, fosite.AccessResponder)
	NewRevocationRequest(context.Context, *http.Request) error
	WriteRevocationResponse(context.Context, http.ResponseWriter, error)
	IntrospectToken(context.Context, string, fosite.TokenUse, fosite.Session, ...string) (fosite.TokenUse, fosite.AccessRequester, error)
}

// NewServer validates local configuration and composes the constrained Fosite provider.
func NewServer(pool *pgxpool.Pool, clients *ClientRegistry, sessions WebsiteSessions, cfg Config) (*Server, error) {
	if clients == nil {
		return nil, errors.New("OAuth client registry is required")
	}
	if sessions == nil {
		return nil, errors.New("website sessions are required")
	}
	if cfg.ConsentLifetime == 0 {
		cfg.ConsentLifetime = defaultConsentLifetime
	}
	if err := validateServerConfig(cfg); err != nil {
		return nil, err
	}
	if clients.resource != cfg.Resource {
		return nil, errors.New("OAuth client registry resource does not match server resource")
	}
	fositeConfig := &fosite.Config{
		AccessTokenLifespan:            15 * time.Minute,
		AuthorizeCodeLifespan:          5 * time.Minute,
		RefreshTokenLifespan:           30 * 24 * time.Hour,
		EnforcePKCE:                    true,
		EnforcePKCEForPublicClients:    true,
		EnablePKCEPlainChallengeMethod: false,
		ScopeStrategy:                  fosite.ExactScopeStrategy,
		AudienceMatchingStrategy:       fosite.ExactAudienceMatchingStrategy,
		RefreshTokenScopes:             []string{},
		SendDebugMessagesToClients:     false,
		SanitationWhiteList:            []string{"redirect_uri", "resource"},
		GlobalSecret:                   append([]byte(nil), cfg.GlobalSecret...),
		RotatedGlobalSecrets:           cloneSecrets(cfg.RotatedGlobalSecrets),
		AccessTokenIssuer:              cfg.Issuer,
		TokenURL:                       cfg.Issuer + "/oauth/token",
	}
	strategy := compose.NewOAuth2HMACStrategy(fositeConfig)
	store, err := NewStore(pool, clients, strategy)
	if err != nil {
		return nil, err
	}
	provider := compose.Compose(
		fositeConfig,
		store,
		strategy,
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2TokenIntrospectionFactory,
		compose.OAuth2TokenRevocationFactory,
		compose.OAuth2PKCEFactory,
	)
	return &Server{
		clients: clients, sessions: sessions, provider: provider, cfg: cfg, now: time.Now,
		beginOperation: func(ctx context.Context, binding Binding, kind CredentialKind, presented string) (protocolOperation, error) {
			return store.Begin(ctx, binding, kind, presented)
		},
		createPendingConsent: store.CreatePendingConsent,
		bindPendingConsent: store.BindPendingConsent,
		readPendingConsent: store.ReadPendingConsent,
		consumePendingConsent: store.ConsumePendingConsent,
	}, nil
}

// Register installs the public OAuth and consent routes.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleAuthorizationServerMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.handleProtectedResourceMetadata)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", s.handleProtectedResourceMetadata)
	mux.HandleFunc("GET /oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("GET /oauth/resume", s.handleResume)
	mux.HandleFunc("POST /oauth/authorize/decision", s.handleDecision)
	mux.HandleFunc("POST /oauth/token", s.handleToken)
	mux.HandleFunc("POST /oauth/revoke", s.handleRevoke)
}

func cloneSecrets(values [][]byte) [][]byte {
	clone := make([][]byte, len(values))
	for index := range values {
		clone[index] = append([]byte(nil), values[index]...)
	}
	return clone
}

func noStoreOAuth(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func oauthFailure(w http.ResponseWriter, status int) {
	noStoreOAuth(w)
	http.Error(w, http.StatusText(status), status)
}

func oauthJSONError(w http.ResponseWriter, status int, name string) {
	writeJSON(w, status, map[string]string{"error": name})
}
