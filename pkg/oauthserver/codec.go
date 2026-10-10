package oauthserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ory/fosite"
)

const storedRequestVersion = 1

var storedFormKeys = map[string]struct{}{
	"grant_type":            {},
	"response_type":         {},
	"scope":                 {},
	"client_id":             {},
	"redirect_uri":          {},
	"resource":              {},
	"code_challenge":        {},
	"code_challenge_method": {},
}

// StoredRequest is the bounded, versioned Fosite request snapshot.
type StoredRequest struct {
	Version           int           `json:"version"`
	ID                string        `json:"id"`
	RequestedAt       time.Time     `json:"requested_at"`
	ClientID          string        `json:"client_id"`
	RequestedScopes   []string      `json:"requested_scopes"`
	GrantedScopes     []string      `json:"granted_scopes"`
	RequestedAudience []string      `json:"requested_audience"`
	GrantedAudience   []string      `json:"granted_audience"`
	Form              url.Values    `json:"form"`
	Session           StoredSession `json:"session"`
}

// StoredSession is the bounded session portion of a request snapshot.
type StoredSession struct {
	ExpiresAt    map[fosite.TokenType]time.Time `json:"expires_at"`
	Subject      string                         `json:"subject"`
	AccountID    int64                          `json:"account_id"`
	AccountEpoch int64                          `json:"account_epoch"`
	Resource     string                         `json:"resource"`
}

// RequestCodec converts Fosite request values to and from bounded snapshots.
type RequestCodec struct {
	clients *ClientRegistry
}

func NewRequestCodec(clients *ClientRegistry) *RequestCodec {
	return &RequestCodec{clients: clients}
}

func (c *RequestCodec) Encode(request fosite.Requester) ([]byte, error) {
	if request == nil {
		return nil, errors.New("encode OAuth request: request is nil")
	}
	if c == nil || c.clients == nil {
		return nil, errors.New("encode OAuth request: client registry is nil")
	}
	client := request.GetClient()
	if client == nil {
		return nil, errors.New("encode OAuth request: client is nil")
	}
	if _, err := c.clients.GetClient(context.Background(), client.GetID()); err != nil {
		return nil, fmt.Errorf("encode OAuth request: load current client: %w", err)
	}
	session, ok := request.GetSession().(*Session)
	if !ok || session == nil {
		return nil, fmt.Errorf("encode OAuth request: session type %T is unsupported", request.GetSession())
	}
	if session.DefaultSession == nil {
		return nil, errors.New("encode OAuth request: default session is nil")
	}
	if session.GetUsername() != "" {
		return nil, errors.New("encode OAuth request: session username must be empty")
	}
	if len(session.DefaultSession.Extra) != 0 {
		return nil, errors.New("encode OAuth request: session extra claims must be empty")
	}

	stored := StoredRequest{
		Version:           storedRequestVersion,
		ID:                request.GetID(),
		RequestedAt:       request.GetRequestedAt(),
		ClientID:          client.GetID(),
		RequestedScopes:   append([]string(nil), request.GetRequestedScopes()...),
		GrantedScopes:     append([]string(nil), request.GetGrantedScopes()...),
		RequestedAudience: append([]string(nil), request.GetRequestedAudience()...),
		GrantedAudience:   append([]string(nil), request.GetGrantedAudience()...),
		Form:              filterStoredForm(request.GetRequestForm()),
		Session: StoredSession{
			ExpiresAt:    cloneExpirationMap(session.DefaultSession.ExpiresAt),
			Subject:      session.GetSubject(),
			AccountID:    session.AccountID,
			AccountEpoch: session.AccountEpoch,
			Resource:     session.Resource,
		},
	}
	if err := c.validate(stored); err != nil {
		return nil, fmt.Errorf("encode OAuth request: %w", err)
	}
	payload, err := json.Marshal(stored)
	if err != nil {
		return nil, fmt.Errorf("encode OAuth request: %w", err)
	}
	return payload, nil
}

func (c *RequestCodec) Decode(payload []byte, target fosite.Session) (*fosite.Request, error) {
	if c == nil || c.clients == nil {
		return nil, errors.New("decode OAuth request: client registry is nil")
	}
	var stored StoredRequest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stored); err != nil {
		return nil, fmt.Errorf("decode OAuth request: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode OAuth request: %w", err)
	}
	if err := c.validate(stored); err != nil {
		return nil, fmt.Errorf("decode OAuth request: %w", err)
	}
	client, err := c.clients.GetClient(context.Background(), stored.ClientID)
	if err != nil {
		return nil, fmt.Errorf("decode OAuth request: load current client: %w", err)
	}

	session, err := hydrateSession(target, stored.Session)
	if err != nil {
		return nil, fmt.Errorf("decode OAuth request: %w", err)
	}
	return &fosite.Request{
		ID:                stored.ID,
		RequestedAt:       stored.RequestedAt,
		Client:            client,
		RequestedScope:    fosite.Arguments(append([]string(nil), stored.RequestedScopes...)),
		GrantedScope:      fosite.Arguments(append([]string(nil), stored.GrantedScopes...)),
		RequestedAudience: fosite.Arguments(append([]string(nil), stored.RequestedAudience...)),
		GrantedAudience:   fosite.Arguments(append([]string(nil), stored.GrantedAudience...)),
		Form:              cloneForm(stored.Form),
		Session:           session,
	}, nil
}

func (c *RequestCodec) validate(stored StoredRequest) error {
	if stored.Version != storedRequestVersion {
		return fmt.Errorf("stored request version %d is unsupported", stored.Version)
	}
	if strings.TrimSpace(stored.ID) == "" {
		return errors.New("request ID must not be empty")
	}
	if stored.RequestedAt.IsZero() {
		return errors.New("requested time must not be zero")
	}
	if strings.TrimSpace(stored.ClientID) == "" {
		return errors.New("client ID must not be empty")
	}
	client, err := c.clients.GetClient(context.Background(), stored.ClientID)
	if err != nil {
		return fmt.Errorf("load current client: %w", err)
	}
	if !exactStringSet(stored.RequestedScopes, "mcp:read") {
		return errors.New("requested scopes must contain mcp:read exactly once")
	}
	if len(stored.GrantedScopes) != 0 && !exactStringSet(stored.GrantedScopes, "mcp:read") {
		return errors.New("granted scopes must be empty or contain mcp:read exactly once")
	}
	if !exactStringSet(stored.RequestedAudience, c.clients.resource) {
		return errors.New("requested audience must equal the configured resource")
	}
	if len(stored.GrantedAudience) != 0 && !exactStringSet(stored.GrantedAudience, c.clients.resource) {
		return errors.New("granted audience must be empty or equal the configured resource")
	}
	if err := validateStoredSession(stored.Session, c.clients.resource); err != nil {
		return err
	}
	if err := validateStoredForm(stored.Form, stored, client); err != nil {
		return err
	}
	return nil
}

func validateStoredSession(session StoredSession, resource string) error {
	if session.AccountID <= 0 {
		return errors.New("session account ID must be positive")
	}
	if session.AccountEpoch < 1 {
		return errors.New("session account epoch must be positive")
	}
	if session.Subject != strconv.FormatInt(session.AccountID, 10) {
		return errors.New("session subject does not match account ID")
	}
	if session.Resource != resource {
		return errors.New("session resource does not match configured resource")
	}
	if session.ExpiresAt == nil {
		return errors.New("session expiration map must not be nil")
	}
	for tokenType, expiresAt := range session.ExpiresAt {
		switch tokenType {
		case fosite.AuthorizeCode, fosite.AccessToken, fosite.RefreshToken:
		default:
			return fmt.Errorf("session token type %q is unsupported", tokenType)
		}
		if expiresAt.IsZero() {
			return fmt.Errorf("session expiration for %q must not be zero", tokenType)
		}
	}
	return nil
}

func validateStoredForm(form url.Values, stored StoredRequest, client fosite.Client) error {
	if form == nil {
		return errors.New("request form must not be nil")
	}
	for key, values := range form {
		if _, allowed := storedFormKeys[key]; !allowed {
			return fmt.Errorf("request form field %q is not allowed", key)
		}
		if len(values) != 1 {
			return fmt.Errorf("request form field %q must have exactly one value", key)
		}
		if values[0] == "" {
			return fmt.Errorf("request form field %q must not be empty", key)
		}
	}
	if value := form.Get("client_id"); value != "" && value != stored.ClientID {
		return errors.New("request form client_id does not match client ID")
	}
	if value := form.Get("scope"); value != "" && value != strings.Join(stored.RequestedScopes, " ") {
		return errors.New("request form scope does not match requested scopes")
	}
	if value := form.Get("resource"); value != "" && value != stored.Session.Resource {
		return errors.New("request form resource does not match session resource")
	}
	if value := form.Get("redirect_uri"); value != "" && !containsString(client.GetRedirectURIs(), value) {
		return errors.New("request form redirect_uri is not currently registered")
	}
	if value := form.Get("grant_type"); value != "" && value != "authorization_code" && value != "refresh_token" {
		return errors.New("request form grant_type is unsupported")
	}
	if value := form.Get("response_type"); value != "" && value != "code" {
		return errors.New("request form response_type is unsupported")
	}
	challenge := form.Get("code_challenge")
	method := form.Get("code_challenge_method")
	if challenge != "" && method != "S256" {
		return errors.New("request form code challenge method must be S256")
	}
	if challenge == "" && method != "" {
		return errors.New("request form code challenge is missing")
	}
	return nil
}

func hydrateSession(target fosite.Session, stored StoredSession) (*Session, error) {
	var session *Session
	if target == nil {
		session = &Session{}
	} else {
		var ok bool
		session, ok = target.(*Session)
		if !ok || session == nil {
			return nil, fmt.Errorf("session type %T is unsupported", target)
		}
	}
	session.DefaultSession = &fosite.DefaultSession{
		ExpiresAt: cloneExpirationMap(stored.ExpiresAt),
		Subject:   stored.Subject,
	}
	session.AccountID = stored.AccountID
	session.AccountEpoch = stored.AccountEpoch
	session.Resource = stored.Resource
	return session, nil
}

func filterStoredForm(form url.Values) url.Values {
	filtered := make(url.Values)
	for key, values := range form {
		if _, allowed := storedFormKeys[key]; allowed {
			filtered[key] = append([]string(nil), values...)
		}
	}
	return filtered
}

func cloneForm(form url.Values) url.Values {
	clone := make(url.Values, len(form))
	for key, values := range form {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

func cloneExpirationMap(expiresAt map[fosite.TokenType]time.Time) map[fosite.TokenType]time.Time {
	clone := make(map[fosite.TokenType]time.Time, len(expiresAt))
	for tokenType, expiration := range expiresAt {
		clone[tokenType] = expiration
	}
	return clone
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
