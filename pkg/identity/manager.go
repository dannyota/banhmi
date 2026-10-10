package identity

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	LoginCookieName          = "__Host-banhmi-login"
	SessionCookieName        = "__Host-banhmi-session"
	PendingConsentCookieName = "__Host-banhmi-consent"

	loginLifetime   = 10 * time.Minute
	sessionLifetime = 12 * time.Hour
	providerTimeout = 5 * time.Second
	randomValueSize = 32
	pkcePurpose     = "google-login-pkce"
	csrfHeader      = "X-CSRF-Token"
)

// Option configures a Manager without changing its local configuration contract.
type Option func(*managerOptions) error

type managerOptions struct {
	provider loginProvider
	now      func() time.Time
}

func withProvider(provider loginProvider) Option {
	return func(options *managerOptions) error {
		if provider == nil {
			return fmt.Errorf("login provider is required")
		}
		options.provider = provider
		return nil
	}
}

func withClock(now func() time.Time) Option {
	return func(options *managerOptions) error {
		if now == nil {
			return fmt.Errorf("clock is required")
		}
		options.now = now
		return nil
	}
}

// Manager handles Google login and database-backed website sessions.
type Manager struct {
	store Store
	cfg   Config
	now   func() time.Time

	providerMu      sync.Mutex
	provider        loginProvider
	resolveProvider func(context.Context) (loginProvider, error)
	discoveryTimeout time.Duration
}

// NewManager constructs a manager without contacting Google or the database.
func NewManager(store Store, cfg Config, options ...Option) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("identity store is required")
	}
	if cfg.PublicOrigin == nil {
		return nil, fmt.Errorf("public origin is required")
	}
	parsedOrigin, err := parsePublicOrigin(cfg.PublicOrigin.String())
	if err != nil || parsedOrigin.String() != cfg.PublicOrigin.String() {
		return nil, fmt.Errorf("public origin is invalid")
	}
	if strings.TrimSpace(cfg.GoogleClientID) == "" || strings.TrimSpace(cfg.GoogleClientSecret) == "" {
		return nil, fmt.Errorf("Google client configuration is required")
	}
	if strings.TrimSpace(cfg.AuthDatabaseURL) == "" {
		return nil, fmt.Errorf("authentication database URL is required")
	}
	if cfg.TransactionKey == ([32]byte{}) {
		return nil, fmt.Errorf("transaction key is required")
	}

	settings := managerOptions{now: time.Now}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("manager option is nil")
		}
		if err := option(&settings); err != nil {
			return nil, fmt.Errorf("apply manager option: %w", err)
		}
	}
	m := &Manager{
		store: store, cfg: cfg, now: settings.now, provider: settings.provider,
		discoveryTimeout: providerTimeout,
	}
	m.resolveProvider = func(ctx context.Context) (loginProvider, error) {
		return discoverGoogle(ctx, cfg)
	}
	return m, nil
}

func (m *Manager) loginProvider(ctx context.Context) (loginProvider, error) {
	m.providerMu.Lock()
	cached := m.provider
	m.providerMu.Unlock()
	if cached != nil {
		return cached, nil
	}

	bounded, cancel := context.WithTimeout(ctx, m.discoveryTimeout)
	defer cancel()
	provider, err := m.resolveProvider(bounded)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, fmt.Errorf("discovered login provider is nil")
	}

	m.providerMu.Lock()
	defer m.providerMu.Unlock()
	if m.provider != nil {
		return m.provider, nil
	}
	m.provider = provider
	return provider, nil
}

// Authenticate loads the current session and account state from the store.
func (m *Manager) Authenticate(r *http.Request) (Principal, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return Principal{}, ErrUnauthenticated
	}
	raw, err := decodeToken(cookie.Value)
	if err != nil || len(raw) != randomValueSize {
		return Principal{}, ErrUnauthenticated
	}
	return m.store.Session(r.Context(), hash(raw), m.now())
}

// CSRFToken returns the deterministic token bound to the current opaque session.
func (m *Manager) CSRFToken(r *http.Request) string {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return ""
	}
	raw, err := decodeToken(cookie.Value)
	if err != nil || len(raw) != randomValueSize {
		return ""
	}
	return m.csrfForSession(raw)
}

// ValidCSRF checks a token against the current opaque session.
func (m *Manager) ValidCSRF(r *http.Request, token string) bool {
	expected := m.CSRFToken(r)
	return expected != "" && token != "" && equalSecret(expected, token)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return encodeToken(sum[:])
}
