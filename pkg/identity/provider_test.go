package identity

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

func TestGoogleProviderVerifySignedIDToken(t *testing.T) {
	provider, privateKey := signedTestProvider(t, "client-id")
	now := time.Now()
	token := signIDToken(t, privateKey, map[string]any{
		"iss": googleIssuer, "sub": "CaseSensitiveSubject", "aud": "client-id",
		"exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce",
		"email": "person@example.com", "email_verified": true, "name": "Person",
	})

	claims, err := provider.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.Subject != "CaseSensitiveSubject" || claims.Nonce != "nonce" || !claims.EmailVerified {
		t.Fatalf("Verify() claims = %+v", claims)
	}
}

func TestGoogleProviderCanonicalizesVerifiedBareGoogleIssuer(t *testing.T) {
	provider, privateKey := signedTestProvider(t, "client-id")
	now := time.Now()
	token := signIDToken(t, privateKey, map[string]any{
		"iss": "accounts.google.com", "sub": "subject", "aud": "client-id",
		"exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce",
		"email": "person@example.com", "email_verified": true,
	})

	claims, err := provider.Verify(context.Background(), token)
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if claims.Issuer != googleIssuer {
		t.Fatalf("Verify() issuer = %q, want %q", claims.Issuer, googleIssuer)
	}
}

func TestGoogleProviderRejectsInvalidSignedIDTokens(t *testing.T) {
	provider, privateKey := signedTestProvider(t, "client-id")
	_, wrongKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	now := time.Now()
	base := map[string]any{
		"iss": googleIssuer, "sub": "subject", "aud": "client-id",
		"exp": now.Add(time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce",
		"email": "person@example.com", "email_verified": true,
	}
	tests := []struct {
		name   string
		key    ed25519.PrivateKey
		mutate func(map[string]any)
	}{
		{name: "signature", key: wrongKey},
		{name: "issuer", key: privateKey, mutate: func(c map[string]any) { c["iss"] = "https://issuer.invalid" }},
		{name: "audience", key: privateKey, mutate: func(c map[string]any) { c["aud"] = "another-client" }},
		{name: "expiry", key: privateKey, mutate: func(c map[string]any) { c["exp"] = now.Add(-time.Minute).Unix() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := cloneClaims(base)
			if test.mutate != nil {
				test.mutate(claims)
			}
			if _, err := provider.Verify(context.Background(), signIDToken(t, test.key, claims)); err == nil {
				t.Fatal("Verify() error = nil")
			}
		})
	}
}

func TestGoogleProviderExchangeUsesPKCEAndRetainsNoAccessToken(t *testing.T) {
	var verifier string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		verifier = r.Form.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"discard-me","token_type":"Bearer","id_token":"signed-id-token"}`))
	}))
	defer server.Close()

	provider := &googleProvider{oauth: oauth2.Config{
		ClientID: "client-id", ClientSecret: "client-secret",
		Endpoint: oauth2.Endpoint{TokenURL: server.URL}, RedirectURL: "https://evidence.example/auth/google/callback",
	}}
	rawIDToken, err := provider.Exchange(context.Background(), "code", "pkce-verifier")
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if rawIDToken != "signed-id-token" || verifier != "pkce-verifier" {
		t.Fatalf("Exchange() = %q, verifier = %q", rawIDToken, verifier)
	}
}

func signedTestProvider(t *testing.T, clientID string) (*googleProvider, ed25519.PrivateKey) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	return &googleProvider{verifier: oidc.NewVerifier(
		googleIssuer,
		&oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{publicKey}},
		&oidc.Config{ClientID: clientID, SupportedSigningAlgs: []string{oidc.EdDSA}},
	)}, privateKey
}

func signIDToken(t *testing.T, key ed25519.PrivateKey, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: key}, nil)
	if err != nil {
		t.Fatalf("NewSigner() error = %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	object, err := signer.Sign(payload)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	compact, err := object.CompactSerialize()
	if err != nil {
		t.Fatalf("CompactSerialize() error = %v", err)
	}
	return compact
}

func cloneClaims(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
