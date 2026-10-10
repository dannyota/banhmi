package oauthserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ory/fosite"
)

const clientRegistryVersion = 1

var ErrClientAssertionsUnsupported = errors.New("client assertion JWTs are unsupported")

type clientRegistryFile struct {
	Version int            `json:"version"`
	Clients []clientConfig `json:"clients"`
}

type clientConfig struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
}

// ClientRegistry holds the current preregistered public-client policy.
type ClientRegistry struct {
	resource string
	clients  map[string]clientConfig
}

var _ fosite.ClientManager = (*ClientRegistry)(nil)

// ParseClientRegistry parses a complete versioned client registry document.
func ParseClientRegistry(data []byte, resource string) (*ClientRegistry, error) {
	if err := validateResource(resource); err != nil {
		return nil, err
	}

	var document clientRegistryFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode client registry: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode client registry: %w", err)
	}
	if document.Version != clientRegistryVersion {
		return nil, fmt.Errorf("client registry version %d is unsupported", document.Version)
	}
	if document.Clients == nil {
		return nil, errors.New("client registry clients must be an array")
	}

	registry := &ClientRegistry{
		resource: resource,
		clients:  make(map[string]clientConfig, len(document.Clients)),
	}
	for index, client := range document.Clients {
		if err := validateClientConfig(client); err != nil {
			return nil, fmt.Errorf("validate client %d: %w", index, err)
		}
		if _, exists := registry.clients[client.ClientID]; exists {
			return nil, fmt.Errorf("client ID %q is duplicated", client.ClientID)
		}
		registry.clients[client.ClientID] = cloneClientConfig(client)
	}
	return registry, nil
}

func (r *ClientRegistry) GetClient(_ context.Context, id string) (fosite.Client, error) {
	client, ok := r.clients[id]
	if !ok {
		return nil, fosite.ErrNotFound
	}
	return &fosite.DefaultClient{
		ID:            client.ClientID,
		RedirectURIs:  append([]string(nil), client.RedirectURIs...),
		GrantTypes:    append([]string(nil), client.GrantTypes...),
		ResponseTypes: append([]string(nil), client.ResponseTypes...),
		Scopes:        []string{client.Scope},
		Audience:      []string{r.resource},
		Public:        true,
	}, nil
}

// ValidateRedirect requires an exact redirect URI registered for the client.
func (r *ClientRegistry) ValidateRedirect(clientID, rawURI string) error {
	client, ok := r.clients[clientID]
	if !ok {
		return fmt.Errorf("validate redirect for client %q: %w", clientID, fosite.ErrNotFound)
	}
	if err := validateRedirectURI(rawURI); err != nil {
		return fmt.Errorf("validate redirect URI: %w", err)
	}
	if !containsString(client.RedirectURIs, rawURI) {
		return errors.New("redirect URI is not registered for the client")
	}
	return nil
}

func (r *ClientRegistry) ClientAssertionJWTValid(context.Context, string) error {
	return ErrClientAssertionsUnsupported
}

func (r *ClientRegistry) SetClientAssertionJWT(context.Context, string, time.Time) error {
	return ErrClientAssertionsUnsupported
}

func validateClientConfig(client clientConfig) error {
	if strings.TrimSpace(client.ClientID) == "" {
		return errors.New("client_id must not be empty")
	}
	if strings.TrimSpace(client.ClientName) == "" {
		return errors.New("client_name must not be empty")
	}
	if len(client.RedirectURIs) == 0 {
		return errors.New("redirect_uris must not be empty")
	}
	redirects := make(map[string]struct{}, len(client.RedirectURIs))
	for _, redirect := range client.RedirectURIs {
		if err := validateRedirectURI(redirect); err != nil {
			return fmt.Errorf("redirect URI %q: %w", redirect, err)
		}
		if _, exists := redirects[redirect]; exists {
			return fmt.Errorf("redirect URI %q is duplicated", redirect)
		}
		redirects[redirect] = struct{}{}
	}
	if client.TokenEndpointAuthMethod != "none" {
		return errors.New("token_endpoint_auth_method must be none")
	}
	if !exactStringSet(client.GrantTypes, "authorization_code", "refresh_token") {
		return errors.New("grant_types must contain authorization_code and refresh_token exactly once")
	}
	if !exactStringSet(client.ResponseTypes, "code") {
		return errors.New("response_types must contain code exactly once")
	}
	if client.Scope != "mcp:read" {
		return errors.New("scope must be mcp:read")
	}
	return nil
}

func validateRedirectURI(raw string) error {
	if raw == "" || strings.Contains(raw, "*") {
		return errors.New("URI must be exact and contain no wildcard")
	}
	if strings.Contains(raw, "#") {
		return errors.New("URI must not contain a fragment delimiter")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse URI: %w", err)
	}
	if !parsed.IsAbs() || parsed.Opaque != "" || parsed.Host == "" {
		return errors.New("URI must be absolute and hierarchical")
	}
	if err := validateURLAuthority(parsed); err != nil {
		return err
	}
	if parsed.User != nil {
		return errors.New("URI must not contain userinfo")
	}
	if parsed.Fragment != "" {
		return errors.New("URI must not contain a fragment")
	}
	if strings.EqualFold(parsed.Hostname(), "localhost") {
		return errors.New("URI must not use localhost")
	}

	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		return validateLoopbackRedirect(parsed)
	default:
		return errors.New("URI must use HTTPS or an allowed HTTP loopback address")
	}
}

func validateLoopbackRedirect(parsed *url.URL) error {
	host := parsed.Hostname()
	address := net.ParseIP(host)
	if address == nil || !(address.Equal(net.ParseIP("127.0.0.1")) || address.Equal(net.ParseIP("::1"))) {
		return errors.New("HTTP redirect host must be 127.0.0.1 or ::1")
	}
	port := parsed.Port()
	if port == "" {
		return errors.New("HTTP loopback redirect must include an explicit port")
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return errors.New("HTTP loopback redirect port is invalid")
	}
	return nil
}

func validateResource(raw string) error {
	if raw == "" || strings.Contains(raw, "*") {
		return errors.New("OAuth resource must be an exact HTTPS URL")
	}
	if strings.ContainsAny(raw, "?#") {
		return errors.New("OAuth resource must not contain a query or fragment delimiter")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse OAuth resource: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("OAuth resource must be an absolute HTTPS URL without userinfo, query, or fragment")
	}
	if err := validateURLAuthority(parsed); err != nil {
		return fmt.Errorf("OAuth resource authority: %w", err)
	}
	if parsed.Path != "/mcp" || parsed.RawPath != "" || parsed.EscapedPath() != "/mcp" {
		return errors.New("OAuth resource path must be exactly /mcp")
	}
	return nil
}

func validateURLAuthority(parsed *url.URL) error {
	if parsed.Hostname() == "" {
		return errors.New("URI authority must contain a hostname")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return errors.New("URI authority must not contain an empty port")
	}
	if port := parsed.Port(); port != "" {
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 {
			return errors.New("URI port must be between 1 and 65535")
		}
	}
	return nil
}

func exactStringSet(actual []string, expected ...string) bool {
	if len(actual) != len(expected) {
		return false
	}
	want := make(map[string]struct{}, len(expected))
	for _, value := range expected {
		want[value] = struct{}{}
	}
	for _, value := range actual {
		if _, ok := want[value]; !ok {
			return false
		}
		delete(want, value)
	}
	return len(want) == 0
}

func cloneClientConfig(client clientConfig) clientConfig {
	client.RedirectURIs = append([]string(nil), client.RedirectURIs...)
	client.GrantTypes = append([]string(nil), client.GrantTypes...)
	client.ResponseTypes = append([]string(nil), client.ResponseTypes...)
	return client
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); err == io.EOF {
		return nil
	} else if err != nil {
		return err
	}
	return errors.New("trailing JSON content")
}
