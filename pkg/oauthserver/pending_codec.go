package oauthserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/ory/fosite"
)

const pendingAuthorizationVersion = 1

var errInvalidTarget = &fosite.RFC6749Error{
	ErrorField: "invalid_target", DescriptionField: "The requested resource is invalid.", CodeField: http.StatusBadRequest,
}

var pendingAuthorizeKeys = map[string]struct{}{
	"client_id":             {},
	"redirect_uri":          {},
	"response_type":         {},
	"scope":                 {},
	"resource":              {},
	"state":                 {},
	"response_mode":         {},
	"code_challenge":        {},
	"code_challenge_method": {},
}

type pendingAuthorization struct {
	Version             int    `json:"version"`
	ClientID            string `json:"client_id"`
	RedirectURI         string `json:"redirect_uri"`
	ResponseType        string `json:"response_type"`
	Scope               string `json:"scope"`
	Resource            string `json:"resource"`
	State               string `json:"state,omitempty"`
	ResponseMode        string `json:"response_mode,omitempty"`
	CodeChallenge       string `json:"code_challenge"`
	CodeChallengeMethod string `json:"code_challenge_method"`
}

func validateAuthorizeParameters(values url.Values, resource string) (pendingAuthorization, error) {
	for key, entries := range values {
		if _, allowed := pendingAuthorizeKeys[key]; !allowed {
			return pendingAuthorization{}, fmt.Errorf("authorization parameter %q is unsupported: %w", key, fosite.ErrInvalidRequest)
		}
		if len(entries) != 1 {
			return pendingAuthorization{}, fmt.Errorf("authorization parameter %q must occur exactly once: %w", key, fosite.ErrInvalidRequest)
		}
	}
	required := []string{"client_id", "redirect_uri", "response_type", "scope", "resource", "code_challenge", "code_challenge_method"}
	for _, key := range required {
		if len(values[key]) != 1 || values[key][0] == "" {
			if key == "resource" {
				return pendingAuthorization{}, fmt.Errorf("authorization parameter %q is required: %w", key, errInvalidTarget)
			}
			return pendingAuthorization{}, fmt.Errorf("authorization parameter %q is required: %w", key, fosite.ErrInvalidRequest)
		}
	}
	pending := pendingAuthorization{
		Version:             pendingAuthorizationVersion,
		ClientID:            values.Get("client_id"),
		RedirectURI:         values.Get("redirect_uri"),
		ResponseType:        values.Get("response_type"),
		Scope:               values.Get("scope"),
		Resource:            values.Get("resource"),
		State:               values.Get("state"),
		ResponseMode:        values.Get("response_mode"),
		CodeChallenge:       values.Get("code_challenge"),
		CodeChallengeMethod: values.Get("code_challenge_method"),
	}
	if entries, present := values["state"]; present && (len(entries) != 1 || entries[0] == "") {
		return pendingAuthorization{}, fmt.Errorf("state must be nonempty when present: %w", fosite.ErrInvalidRequest)
	}
	if pending.ResponseType != "code" {
		return pendingAuthorization{}, fmt.Errorf("response_type must be code: %w", fosite.ErrUnsupportedResponseType)
	}
	if pending.Scope != "mcp:read" {
		return pendingAuthorization{}, fmt.Errorf("scope must be mcp:read: %w", fosite.ErrInvalidScope)
	}
	if pending.Resource != resource {
		return pendingAuthorization{}, fmt.Errorf("resource does not match the configured protected resource: %w", errInvalidTarget)
	}
	if pending.ResponseMode != "" && pending.ResponseMode != "query" {
		return pendingAuthorization{}, fmt.Errorf("response_mode must be absent or query: %w", fosite.ErrUnsupportedResponseMode)
	}
	if pending.CodeChallengeMethod != "S256" {
		return pendingAuthorization{}, fmt.Errorf("code_challenge_method must be S256: %w", fosite.ErrInvalidRequest)
	}
	decodedChallenge, err := base64.RawURLEncoding.Strict().DecodeString(pending.CodeChallenge)
	if err != nil || len(pending.CodeChallenge) != 43 || len(decodedChallenge) != 32 || base64.RawURLEncoding.EncodeToString(decodedChallenge) != pending.CodeChallenge {
		return pendingAuthorization{}, fmt.Errorf("code_challenge must be canonical base64url for 32 bytes: %w", fosite.ErrInvalidRequest)
	}
	return pending, nil
}

func encodePendingAuthorization(values url.Values) ([]byte, error) {
	pending, err := validateAuthorizeParameters(values, values.Get("resource"))
	if err != nil {
		return nil, err
	}
	return json.Marshal(pending)
}

func decodePendingAuthorization(payload []byte) (*http.Request, error) {
	var pending pendingAuthorization
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pending); err != nil {
		return nil, fmt.Errorf("decode pending authorization: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode pending authorization: %w", err)
	}
	if pending.Version != pendingAuthorizationVersion {
		return nil, fmt.Errorf("pending authorization version %d is unsupported", pending.Version)
	}
	values := url.Values{
		"client_id":             {pending.ClientID},
		"redirect_uri":          {pending.RedirectURI},
		"response_type":         {pending.ResponseType},
		"scope":                 {pending.Scope},
		"resource":              {pending.Resource},
		"code_challenge":        {pending.CodeChallenge},
		"code_challenge_method": {pending.CodeChallengeMethod},
	}
	if pending.State != "" {
		values.Set("state", pending.State)
	}
	if pending.ResponseMode != "" {
		values.Set("response_mode", pending.ResponseMode)
	}
	if _, err := validateAuthorizeParameters(values, pending.Resource); err != nil {
		return nil, fmt.Errorf("validate pending authorization: %w", err)
	}
	request, err := http.NewRequest(http.MethodGet, "/oauth/authorize?"+values.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("construct pending authorization request: %w", err)
	}
	return request, nil
}
