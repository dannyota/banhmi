package identity

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu             sync.Mutex
	login          LoginTransaction
	loginCreated   bool
	loginConsumed  bool
	sessions       map[[32]byte]Principal
	createErr      error
	consumeErr     error
	completeErr    error
	sessionErr     error
	revokeErr      error
	completeCount  int
	revokeCount    int
	completeIssuedAt time.Time
	completeExpiresAt time.Time
}

func newMemoryStore() *memoryStore {
	return &memoryStore{sessions: make(map[[32]byte]Principal)}
}

func (s *memoryStore) CreateLogin(_ context.Context, transaction LoginTransaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.createErr != nil {
		return s.createErr
	}
	s.login = transaction
	s.loginCreated = true
	s.loginConsumed = false
	return nil
}

func (s *memoryStore) ConsumeLogin(_ context.Context, stateHash, browserHash []byte, now time.Time) (LoginTransaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumeErr != nil {
		return LoginTransaction{}, s.consumeErr
	}
	if !s.loginCreated || s.loginConsumed || now.After(s.login.ExpiresAt) ||
		!equalBytes(stateHash, s.login.StateHash) || !equalBytes(browserHash, s.login.BrowserHash) {
		return LoginTransaction{}, ErrUnauthenticated
	}
	s.loginConsumed = true
	return s.login, nil
}

func (s *memoryStore) CompleteLogin(_ context.Context, profile Profile, sessionHash []byte, issuedAt time.Time, expiresAt time.Time) (Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.completeErr != nil {
		return Principal{}, s.completeErr
	}
	s.completeCount++
	s.completeIssuedAt = issuedAt
	s.completeExpiresAt = expiresAt
	principal := Principal{AccountID: 42, Email: profile.Email, DisplayName: profile.DisplayName, AuthorizationEpoch: 1, SessionHash: append([]byte(nil), sessionHash...)}
	s.sessions[*(*[32]byte)(sessionHash)] = principal
	return principal, nil
}

func (s *memoryStore) Session(_ context.Context, sessionHash []byte, _ time.Time) (Principal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionErr != nil {
		return Principal{}, s.sessionErr
	}
	if len(sessionHash) != sha256.Size {
		return Principal{}, ErrUnauthenticated
	}
	principal, ok := s.sessions[*(*[32]byte)(sessionHash)]
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return principal, nil
}

func (s *memoryStore) RevokeSession(_ context.Context, sessionHash []byte, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revokeErr != nil {
		return s.revokeErr
	}
	s.revokeCount++
	if len(sessionHash) == sha256.Size {
		delete(s.sessions, *(*[32]byte)(sessionHash))
	}
	return nil
}

type fakeProvider struct {
	authURL      string
	rawIDToken   string
	claims       providerClaims
	exchangeErr  error
	verifyErr    error
	lastVerifier string
}

func (p *fakeProvider) AuthorizationURL(state, nonce, challenge string) string {
	values := url.Values{"state": {state}, "nonce": {nonce}, "code_challenge": {challenge}, "code_challenge_method": {"S256"}}
	return p.authURL + "?" + values.Encode()
}

func (p *fakeProvider) Exchange(_ context.Context, _ string, verifier string) (string, error) {
	p.lastVerifier = verifier
	if p.exchangeErr != nil {
		return "", p.exchangeErr
	}
	return p.rawIDToken, nil
}

func (p *fakeProvider) Verify(_ context.Context, _ string) (providerClaims, error) {
	if p.verifyErr != nil {
		return providerClaims{}, p.verifyErr
	}
	return p.claims, nil
}

func testManager(t *testing.T, store Store, provider loginProvider) *Manager {
	t.Helper()
	origin, err := url.Parse("https://evidence.example")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	cfg := Config{
		PublicOrigin: origin, AuthDatabaseURL: "postgres://runtime.invalid/auth",
		GoogleClientID: "client-id", GoogleClientSecret: "client-secret",
		TransactionKey: [32]byte{1, 2, 3, 4},
	}
	options := []Option{withClock(func() time.Time { return time.Unix(2_000_000_000, 0) })}
	if provider != nil {
		options = append(options, withProvider(provider))
	}
	m, err := NewManager(store, cfg, options...)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return m
}

func TestNewManagerDoesNotDiscoverGoogle(t *testing.T) {
	calls := 0
	m := testManager(t, newMemoryStore(), nil)
	m.resolveProvider = func(context.Context) (loginProvider, error) {
		calls++
		return nil, errors.New("google unavailable")
	}
	if calls != 0 {
		t.Fatalf("discovery calls = %d, want 0", calls)
	}
}

func TestConcurrentDiscoveryDeadlinesAreNotSerialized(t *testing.T) {
	const callers = 4
	m := testManager(t, newMemoryStore(), nil)
	m.discoveryTimeout = 250 * time.Millisecond

	var mu sync.Mutex
	active := 0
	maximumActive := 0
	m.resolveProvider = func(ctx context.Context) (loginProvider, error) {
		mu.Lock()
		active++
		if active > maximumActive {
			maximumActive = active
		}
		mu.Unlock()

		<-ctx.Done()

		mu.Lock()
		active--
		mu.Unlock()
		return nil, ctx.Err()
	}

	results := make(chan error, callers)
	for range callers {
		go func() {
			_, err := m.loginProvider(context.Background())
			results <- err
		}()
	}
	for range callers {
		select {
		case err := <-results:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("loginProvider() error = %v, want deadline exceeded", err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent discovery did not finish within bounded deadlines")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if maximumActive != callers {
		t.Fatalf("maximum concurrent resolvers = %d, want %d", maximumActive, callers)
	}
}

func TestSuccessfulDiscoveryIsCached(t *testing.T) {
	m := testManager(t, newMemoryStore(), nil)
	want := &fakeProvider{authURL: "https://accounts.google.test/auth"}
	calls := 0
	m.resolveProvider = func(context.Context) (loginProvider, error) {
		calls++
		return want, nil
	}

	first, err := m.loginProvider(context.Background())
	if err != nil {
		t.Fatalf("first loginProvider() error = %v", err)
	}
	second, err := m.loginProvider(context.Background())
	if err != nil {
		t.Fatalf("second loginProvider() error = %v", err)
	}
	if first != want || second != want || calls != 1 {
		t.Fatalf("providers = %p, %p; discovery calls = %d", first, second, calls)
	}
}

func TestAuthenticateUsesDatabaseSessionWithoutGoogle(t *testing.T) {
	store := newMemoryStore()
	raw := []byte("0123456789abcdef0123456789abcdef")
	store.sessions[sha256.Sum256(raw)] = Principal{AccountID: 42, SessionHash: hash(raw)}
	m := testManager(t, store, nil)
	m.resolveProvider = func(context.Context) (loginProvider, error) {
		t.Fatal("Authenticate() attempted Google discovery")
		return nil, nil
	}
	r := &http.Request{Header: make(http.Header)}
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: encodeToken(raw)})
	if _, err := m.Authenticate(r); err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
