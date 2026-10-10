package oauthserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
)

func TestRequestCodecPersistsOnlyAllowedFormFields(t *testing.T) {
	codec := testRequestCodec(t)
	request := testRequest(t)
	request.Form.Set("code", "plaintext-code")
	request.Form.Set("code_verifier", "plaintext-verifier")
	request.Form.Set("email", "person@example.com")

	payload, err := codec.Encode(request)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	for _, forbidden := range [][]byte{[]byte("plaintext-code"), []byte("plaintext-verifier"), []byte("person@example.com"), []byte(`"code_verifier"`), []byte(`"email"`)} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("Encode() payload contains forbidden value %q: %s", forbidden, payload)
		}
	}

	decoded, err := codec.Decode(payload, nil)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got := decoded.Form.Get("redirect_uri"); got != "https://client.example/callback" {
		t.Fatalf("decoded redirect_uri = %q, want configured value", got)
	}
	if got := decoded.Form.Get("code_challenge"); got != "challenge" {
		t.Fatalf("decoded code_challenge = %q, want challenge", got)
	}
	if decoded.Form.Has("code") || decoded.Form.Has("code_verifier") || decoded.Form.Has("email") {
		t.Fatalf("decoded form contains forbidden keys: %v", decoded.Form)
	}
}

func TestRequestCodecReloadsCurrentClientPolicy(t *testing.T) {
	codec := testRequestCodec(t)
	payload, err := codec.Encode(testRequest(t))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	emptyRegistry, err := ParseClientRegistry([]byte(`{"version":1,"clients":[]}`), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}

	if _, err := NewRequestCodec(emptyRegistry).Decode(payload, nil); err == nil {
		t.Fatal("Decode() error = nil after client removal, want rejection")
	}
}

func TestRequestCodecHydratesProvidedSession(t *testing.T) {
	codec := testRequestCodec(t)
	request := testRequest(t)
	expires := time.Date(2026, time.October, 11, 10, 0, 0, 0, time.UTC)
	request.Session.SetExpiresAt(fosite.AuthorizeCode, expires)
	payload, err := codec.Encode(request)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	provided := NewSession(99, 99, "https://wrong.example/mcp")
	decoded, err := codec.Decode(payload, provided)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded.GetSession() != provided {
		t.Fatal("Decode() did not retain the caller-provided session")
	}
	if provided.AccountID != 42 || provided.AccountEpoch != 7 || provided.Resource != testResource {
		t.Fatalf("provided identity = (%d, %d, %q), want (42, 7, %q)", provided.AccountID, provided.AccountEpoch, provided.Resource, testResource)
	}
	if got := provided.GetExpiresAt(fosite.AuthorizeCode); !got.Equal(expires) {
		t.Fatalf("provided expiry = %v, want %v", got, expires)
	}
}

func TestRequestCodecRepeatedHydrationPreservesDerivedTokenExpirations(t *testing.T) {
	codec := testRequestCodec(t)
	request := testRequest(t)
	storedCodeExpiry := time.Date(2026, time.October, 11, 10, 0, 0, 0, time.UTC)
	request.Session.SetExpiresAt(fosite.AuthorizeCode, storedCodeExpiry)
	payload, err := codec.Encode(request)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	target := NewSession(99, 99, "https://wrong.example/mcp")
	if _, err := codec.Decode(payload, target); err != nil {
		t.Fatalf("first Decode() error = %v", err)
	}
	derivedAccessExpiry := storedCodeExpiry.Add(15 * time.Minute)
	derivedRefreshExpiry := storedCodeExpiry.Add(30 * 24 * time.Hour)
	targetCodeExpiry := storedCodeExpiry.Add(time.Hour)
	target.SetExpiresAt(fosite.AccessToken, derivedAccessExpiry)
	target.SetExpiresAt(fosite.RefreshToken, derivedRefreshExpiry)
	target.SetExpiresAt(fosite.AuthorizeCode, targetCodeExpiry)
	target.SetExpiresAt(fosite.IDToken, storedCodeExpiry.Add(2*time.Hour))
	firstHydrationExpiries := target.DefaultSession.ExpiresAt
	target.DefaultSession.Username = "discarded-user"
	target.DefaultSession.Extra = map[string]any{"discarded": true}
	target.AccountID = 99
	target.AccountEpoch = 99
	target.Resource = "https://wrong.example/mcp"

	decoded, err := codec.Decode(payload, target)
	if err != nil {
		t.Fatalf("second Decode() error = %v", err)
	}
	if decoded.GetSession() != target {
		t.Fatal("second Decode() did not retain the caller-provided session")
	}
	if got := target.GetExpiresAt(fosite.AccessToken); !got.Equal(derivedAccessExpiry) {
		t.Fatalf("access token expiry = %v, want %v", got, derivedAccessExpiry)
	}
	if got := target.GetExpiresAt(fosite.RefreshToken); !got.Equal(derivedRefreshExpiry) {
		t.Fatalf("refresh token expiry = %v, want %v", got, derivedRefreshExpiry)
	}
	if got := target.GetExpiresAt(fosite.AuthorizeCode); !got.Equal(storedCodeExpiry) {
		t.Fatalf("authorize code expiry = %v, want stored %v", got, storedCodeExpiry)
	}
	if got := target.GetExpiresAt(fosite.IDToken); !got.IsZero() {
		t.Fatalf("ID token expiry = %v, want discarded", got)
	}
	if target.GetUsername() != "" || len(target.DefaultSession.Extra) != 0 {
		t.Fatalf("target default session retained username or extra claims: %#v", target.DefaultSession)
	}
	if target.AccountID != 42 || target.AccountEpoch != 7 || target.Resource != testResource || target.GetSubject() != "42" {
		t.Fatalf("target identity = (%d, %d, %q, %q), want stored identity", target.AccountID, target.AccountEpoch, target.Resource, target.GetSubject())
	}
	secondHydrationExpiries := target.DefaultSession.ExpiresAt
	if _, err := codec.Decode(payload, target); err != nil {
		t.Fatalf("third Decode() error = %v", err)
	}
	if got := target.GetExpiresAt(fosite.AccessToken); !got.Equal(derivedAccessExpiry) {
		t.Fatalf("access token expiry after third hydration = %v, want %v", got, derivedAccessExpiry)
	}
	if got := target.GetExpiresAt(fosite.RefreshToken); !got.Equal(derivedRefreshExpiry) {
		t.Fatalf("refresh token expiry after third hydration = %v, want %v", got, derivedRefreshExpiry)
	}
	if got := target.GetExpiresAt(fosite.AuthorizeCode); !got.Equal(storedCodeExpiry) {
		t.Fatalf("authorize code expiry after third hydration = %v, want stored %v", got, storedCodeExpiry)
	}

	if got := firstHydrationExpiries[fosite.AuthorizeCode]; !got.Equal(targetCodeExpiry) {
		t.Fatalf("source target code expiry mutated to %v, want %v", got, targetCodeExpiry)
	}
	target.SetExpiresAt(fosite.AccessToken, derivedAccessExpiry.Add(time.Hour))
	if got := firstHydrationExpiries[fosite.AccessToken]; !got.Equal(derivedAccessExpiry) {
		t.Fatalf("source target access expiry aliased to %v, want %v", got, derivedAccessExpiry)
	}
	if got := secondHydrationExpiries[fosite.AccessToken]; !got.Equal(derivedAccessExpiry) {
		t.Fatalf("second hydration access expiry aliased to %v, want %v", got, derivedAccessExpiry)
	}
	storedSession := request.Session.(*Session)
	if got := storedSession.GetExpiresAt(fosite.AuthorizeCode); !got.Equal(storedCodeExpiry) {
		t.Fatalf("source stored code expiry mutated to %v, want %v", got, storedCodeExpiry)
	}
	if got := storedSession.GetExpiresAt(fosite.AccessToken); !got.IsZero() {
		t.Fatalf("source stored access expiry = %v, want zero", got)
	}
	if got := storedSession.GetExpiresAt(fosite.RefreshToken); !got.IsZero() {
		t.Fatalf("source stored refresh expiry = %v, want zero", got)
	}
}

func TestRequestCodecHydrationUsesStoredTokenExpirations(t *testing.T) {
	codec := testRequestCodec(t)
	request := testRequest(t)
	storedAccessExpiry := time.Date(2026, time.October, 11, 10, 15, 0, 0, time.UTC)
	storedRefreshExpiry := time.Date(2026, time.November, 10, 10, 0, 0, 0, time.UTC)
	request.Session.SetExpiresAt(fosite.AccessToken, storedAccessExpiry)
	request.Session.SetExpiresAt(fosite.RefreshToken, storedRefreshExpiry)
	payload, err := codec.Encode(request)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	target := NewSession(99, 99, "https://wrong.example/mcp")
	target.SetExpiresAt(fosite.AccessToken, storedAccessExpiry.Add(time.Hour))
	target.SetExpiresAt(fosite.RefreshToken, storedRefreshExpiry.Add(time.Hour))
	if _, err := codec.Decode(payload, target); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if got := target.GetExpiresAt(fosite.AccessToken); !got.Equal(storedAccessExpiry) {
		t.Fatalf("access token expiry = %v, want stored %v", got, storedAccessExpiry)
	}
	if got := target.GetExpiresAt(fosite.RefreshToken); !got.Equal(storedRefreshExpiry) {
		t.Fatalf("refresh token expiry = %v, want stored %v", got, storedRefreshExpiry)
	}
}

func TestRequestCodecHydrationOmitsZeroTargetTokenExpirations(t *testing.T) {
	codec := testRequestCodec(t)
	payload, err := codec.Encode(testRequest(t))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	target := NewSession(99, 99, "https://wrong.example/mcp")
	target.SetExpiresAt(fosite.AccessToken, time.Time{})
	target.SetExpiresAt(fosite.RefreshToken, time.Time{})

	if _, err := codec.Decode(payload, target); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if _, exists := target.DefaultSession.ExpiresAt[fosite.AccessToken]; exists {
		t.Fatal("Decode() retained a zero access token expiry")
	}
	if _, exists := target.DefaultSession.ExpiresAt[fosite.RefreshToken]; exists {
		t.Fatal("Decode() retained a zero refresh token expiry")
	}
}

func TestRequestCodecRejectsMalformedSnapshot(t *testing.T) {
	codec := testRequestCodec(t)
	payload, err := codec.Encode(testRequest(t))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	tests := map[string]func(map[string]any){
		"version":    func(snapshot map[string]any) { snapshot["version"] = float64(2) },
		"request id": func(snapshot map[string]any) { snapshot["id"] = "" },
		"client id form mismatch": func(snapshot map[string]any) {
			snapshot["form"].(map[string]any)["client_id"] = []any{"different-client"}
		},
		"resource form mismatch": func(snapshot map[string]any) {
			snapshot["form"].(map[string]any)["resource"] = []any{"https://wrong.example/mcp"}
		},
		"multiple resources": func(snapshot map[string]any) {
			snapshot["form"].(map[string]any)["resource"] = []any{testResource, testResource}
		},
		"subject mismatch": func(snapshot map[string]any) {
			snapshot["session"].(map[string]any)["subject"] = "41"
		},
		"zero account epoch": func(snapshot map[string]any) {
			snapshot["session"].(map[string]any)["account_epoch"] = float64(0)
		},
		"negative account epoch": func(snapshot map[string]any) {
			snapshot["session"].(map[string]any)["account_epoch"] = float64(-1)
		},
		"audience mismatch": func(snapshot map[string]any) {
			snapshot["granted_audience"] = []any{"https://wrong.example/mcp"}
		},
		"unknown form field": func(snapshot map[string]any) {
			snapshot["form"].(map[string]any)["code"] = []any{"plaintext-code"}
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			var snapshot map[string]any
			if err := json.Unmarshal(payload, &snapshot); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			mutate(snapshot)
			malformed, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			if _, err := codec.Decode(malformed, nil); err == nil {
				t.Fatal("Decode() error = nil, want malformed snapshot rejection")
			}
		})
	}
}

func TestRequestCodecRejectsUnknownFieldsAndTrailingContent(t *testing.T) {
	codec := testRequestCodec(t)
	payload, err := codec.Encode(testRequest(t))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	unknown := append([]byte{}, payload[:len(payload)-1]...)
	unknown = append(unknown, []byte(`,"credential":"secret"}`)...)
	if _, err := codec.Decode(unknown, nil); err == nil {
		t.Fatal("Decode() accepted an unknown field")
	}
	if _, err := codec.Decode(append(payload, []byte(" {}")...), nil); err == nil {
		t.Fatal("Decode() accepted trailing JSON content")
	}
}

func TestRequestCodecRejectsWrongSessionType(t *testing.T) {
	codec := testRequestCodec(t)
	payload, err := codec.Encode(testRequest(t))
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if _, err := codec.Decode(payload, &fosite.DefaultSession{}); err == nil {
		t.Fatal("Decode() accepted an untyped session")
	}
}

func TestRequestCodecRejectsSessionUsername(t *testing.T) {
	codec := testRequestCodec(t)
	request := testRequest(t)
	request.Session.(*Session).DefaultSession.Username = "person@example.com"
	if _, err := codec.Encode(request); err == nil {
		t.Fatal("Encode() accepted a session username")
	}
}

func TestRequestCodecRejectsInvalidAccountEpochOnEncode(t *testing.T) {
	for _, epoch := range []int64{0, -1} {
		t.Run(fmt.Sprintf("epoch %d", epoch), func(t *testing.T) {
			codec := testRequestCodec(t)
			request := testRequest(t)
			request.Session.(*Session).AccountEpoch = epoch
			if _, err := codec.Encode(request); err == nil {
				t.Fatal("Encode() error = nil, want account epoch rejection")
			}
		})
	}
}

func TestRequestCodecRejectsExtraSessionClaims(t *testing.T) {
	codec := testRequestCodec(t)
	request := testRequest(t)
	request.Session.(*Session).DefaultSession.Extra = map[string]any{"credential": "secret"}
	if _, err := codec.Encode(request); err == nil {
		t.Fatal("Encode() accepted extra session claims")
	}
}

func testRequestCodec(t *testing.T) *RequestCodec {
	t.Helper()
	registry, err := ParseClientRegistry([]byte(clientRegistryJSON(clientJSON("client", "https://client.example/callback"))), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	return NewRequestCodec(registry)
}

func testRequest(t *testing.T) *fosite.Request {
	t.Helper()
	registry, err := ParseClientRegistry([]byte(clientRegistryJSON(clientJSON("client", "https://client.example/callback"))), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	client, err := registry.GetClient(t.Context(), "client")
	if err != nil {
		t.Fatalf("GetClient() error = %v", err)
	}
	return &fosite.Request{
		ID:                "request-id",
		RequestedAt:       time.Date(2026, time.October, 11, 9, 0, 0, 0, time.UTC),
		Client:            client,
		RequestedScope:    fosite.Arguments{"mcp:read"},
		GrantedScope:      fosite.Arguments{"mcp:read"},
		RequestedAudience: fosite.Arguments{testResource},
		GrantedAudience:   fosite.Arguments{testResource},
		Form: url.Values{
			"grant_type":            {"authorization_code"},
			"response_type":         {"code"},
			"scope":                 {"mcp:read"},
			"client_id":             {"client"},
			"redirect_uri":          {"https://client.example/callback"},
			"resource":              {testResource},
			"code_challenge":        {"challenge"},
			"code_challenge_method": {"S256"},
		},
		Session: NewSession(42, 7, testResource),
	}
}
