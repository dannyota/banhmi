package oauthserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ory/fosite"
)

const testResource = "https://evidence.example/mcp"

func TestParseClientRegistryBuildsPublicClient(t *testing.T) {
	registry, err := ParseClientRegistry([]byte(`{
		"version": 1,
		"clients": [{
			"client_id": "approved-client",
			"client_name": "Approved client",
			"redirect_uris": ["https://client.example/oauth/callback", "http://127.0.0.1:49152/callback"],
			"token_endpoint_auth_method": "none",
			"grant_types": ["authorization_code", "refresh_token"],
			"response_types": ["code"],
			"scope": "mcp:read"
		}]
	}`), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}

	client, err := registry.GetClient(context.Background(), "approved-client")
	if err != nil {
		t.Fatalf("GetClient() error = %v", err)
	}
	if client.GetID() != "approved-client" {
		t.Fatalf("GetID() = %q, want approved-client", client.GetID())
	}
	if !client.IsPublic() {
		t.Fatal("IsPublic() = false, want true")
	}
	if len(client.GetHashedSecret()) != 0 {
		t.Fatal("GetHashedSecret() returned a secret for a public client")
	}
	if got := []string(client.GetAudience()); len(got) != 1 || got[0] != testResource {
		t.Fatalf("GetAudience() = %q, want [%q]", got, testResource)
	}
	if got := []string(client.GetScopes()); len(got) != 1 || got[0] != "mcp:read" {
		t.Fatalf("GetScopes() = %q, want [mcp:read]", got)
	}
}

func TestParseClientRegistryAcceptsEmptyList(t *testing.T) {
	registry, err := ParseClientRegistry([]byte(`{"version":1,"clients":[]}`), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	if _, err := registry.GetClient(context.Background(), "missing"); !errors.Is(err, fosite.ErrNotFound) {
		t.Fatalf("GetClient() error = %v, want fosite.ErrNotFound", err)
	}
}

func TestParseClientRegistryRejectsMalformedConfiguration(t *testing.T) {
	tests := map[string]string{
		"unknown version":       `{"version":2,"clients":[]}`,
		"unknown field":         `{"version":1,"clients":[],"secret":"value"}`,
		"trailing content":      `{"version":1,"clients":[]} {}`,
		"duplicate client":      clientRegistryJSON(clientJSON("same", "https://one.example/callback"), clientJSON("same", "https://two.example/callback")),
		"empty client id":       clientRegistryJSON(clientJSON("", "https://client.example/callback")),
		"empty client name":     clientRegistryJSON(`{"client_id":"id","client_name":"","redirect_uris":["https://client.example/callback"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"scope":"mcp:read"}`),
		"empty redirect list":   clientRegistryJSON(`{"client_id":"id","client_name":"name","redirect_uris":[],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"scope":"mcp:read"}`),
		"wrong auth method":     clientRegistryJSON(clientJSONWith("client_secret_post", `["authorization_code","refresh_token"]`, `["code"]`, "mcp:read")),
		"missing refresh grant": clientRegistryJSON(clientJSONWith("none", `["authorization_code"]`, `["code"]`, "mcp:read")),
		"extra grant":           clientRegistryJSON(clientJSONWith("none", `["authorization_code","refresh_token","client_credentials"]`, `["code"]`, "mcp:read")),
		"duplicate grant":       clientRegistryJSON(clientJSONWith("none", `["authorization_code","authorization_code","refresh_token"]`, `["code"]`, "mcp:read")),
		"wrong response":        clientRegistryJSON(clientJSONWith("none", `["authorization_code","refresh_token"]`, `["token"]`, "mcp:read")),
		"wrong scope":           clientRegistryJSON(clientJSONWith("none", `["authorization_code","refresh_token"]`, `["code"]`, "openid")),
		"invalid resource":      `RESOURCE`,
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			resource := testResource
			if input == "RESOURCE" {
				input = `{"version":1,"clients":[]}`
				resource = "http://evidence.example/mcp"
			}
			if _, err := ParseClientRegistry([]byte(input), resource); err == nil {
				t.Fatal("ParseClientRegistry() error = nil, want rejection")
			}
		})
	}
}

func TestParseClientRegistryRejectsUnsafeRedirects(t *testing.T) {
	redirects := []string{
		"http://client.example/callback",
		"http://localhost:49152/callback",
		"https://localhost/callback",
		"http://127.0.0.1/callback",
		"http://127.0.0.2:49152/callback",
		"http://[::1]/callback",
		"https://user@client.example/callback",
		"https://client.example/callback#fragment",
		"https://client.example/callback#",
		"https://:443/callback",
		"https://client.example:/callback",
		"https://client.example:99999/callback",
		"https://*.example/callback",
		"/relative/callback",
	}
	for _, redirect := range redirects {
		t.Run(redirect, func(t *testing.T) {
			input := clientRegistryJSON(clientJSON("client", redirect))
			if _, err := ParseClientRegistry([]byte(input), testResource); err == nil {
				t.Fatal("ParseClientRegistry() error = nil, want unsafe redirect rejection")
			}
		})
	}
}

func TestParseClientRegistryRejectsInvalidResources(t *testing.T) {
	resources := []string{
		"https://evidence.example/other",
		"https://evidence.example/mcp/",
		"https://evidence.example/mcp?",
		"https://evidence.example/mcp#",
		"https://evidence.example/mc%70",
		"https://:443/mcp",
		"https://evidence.example:/mcp",
		"https://evidence.example:99999/mcp",
		"https://user@evidence.example/mcp",
		"https://*.example/mcp",
	}
	for _, resource := range resources {
		t.Run(resource, func(t *testing.T) {
			if _, err := ParseClientRegistry([]byte(`{"version":1,"clients":[]}`), resource); err == nil {
				t.Fatal("ParseClientRegistry() error = nil, want invalid resource rejection")
			}
		})
	}
}

func TestParseClientRegistryAcceptsIPv6LoopbackWithPort(t *testing.T) {
	input := clientRegistryJSON(clientJSON("client", "http://[::1]:49152/callback"))
	if _, err := ParseClientRegistry([]byte(input), testResource); err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
}

func TestClientRegistryReturnsIndependentClientCopies(t *testing.T) {
	registry, err := ParseClientRegistry([]byte(clientRegistryJSON(clientJSON("client", "https://client.example/callback"))), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	first, err := registry.GetClient(context.Background(), "client")
	if err != nil {
		t.Fatalf("GetClient() error = %v", err)
	}
	first.(*fosite.DefaultClient).RedirectURIs[0] = "https://attacker.example/callback"

	second, err := registry.GetClient(context.Background(), "client")
	if err != nil {
		t.Fatalf("GetClient() error = %v", err)
	}
	if got := second.GetRedirectURIs()[0]; got != "https://client.example/callback" {
		t.Fatalf("GetRedirectURIs()[0] = %q, want configured redirect", got)
	}
}

func TestClientRegistryValidatesExactRedirect(t *testing.T) {
	registry, err := ParseClientRegistry([]byte(clientRegistryJSON(clientJSON("client", "http://127.0.0.1:49152/callback"))), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	if err := registry.ValidateRedirect("client", "http://127.0.0.1:49152/callback"); err != nil {
		t.Fatalf("ValidateRedirect() error = %v", err)
	}

	tests := map[string]struct {
		clientID string
		redirect string
	}{
		"changed loopback port": {clientID: "client", redirect: "http://127.0.0.1:49153/callback"},
		"unknown client":        {clientID: "missing", redirect: "http://127.0.0.1:49152/callback"},
		"unregistered URI":      {clientID: "client", redirect: "https://client.example/callback"},
		"malformed URI":         {clientID: "client", redirect: "://"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if err := registry.ValidateRedirect(test.clientID, test.redirect); err == nil {
				t.Fatal("ValidateRedirect() error = nil, want exact redirect rejection")
			}
		})
	}
}

func TestClientRegistryRejectsClientAssertionStorage(t *testing.T) {
	registry, err := ParseClientRegistry([]byte(`{"version":1,"clients":[]}`), testResource)
	if err != nil {
		t.Fatalf("ParseClientRegistry() error = %v", err)
	}
	if err := registry.ClientAssertionJWTValid(context.Background(), "jti"); !errors.Is(err, ErrClientAssertionsUnsupported) {
		t.Fatalf("ClientAssertionJWTValid() error = %v, want unsupported", err)
	}
	if err := registry.SetClientAssertionJWT(context.Background(), "jti", time.Now()); !errors.Is(err, ErrClientAssertionsUnsupported) {
		t.Fatalf("SetClientAssertionJWT() error = %v, want unsupported", err)
	}
}

func clientRegistryJSON(clients ...string) string {
	joined := ""
	for i, client := range clients {
		if i > 0 {
			joined += ","
		}
		joined += client
	}
	return `{"version":1,"clients":[` + joined + `]}`
}

func clientJSON(id, redirect string) string {
	return `{"client_id":"` + id + `","client_name":"Client name","redirect_uris":["` + redirect + `"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"],"response_types":["code"],"scope":"mcp:read"}`
}

func clientJSONWith(method, grants, responses, scope string) string {
	return `{"client_id":"client","client_name":"Client name","redirect_uris":["https://client.example/callback"],"token_endpoint_auth_method":"` + method + `","grant_types":` + grants + `,"response_types":` + responses + `,"scope":"` + scope + `"}`
}
