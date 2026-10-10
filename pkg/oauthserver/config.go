package oauthserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	oauthHMACKeyEnv            = "BANHMI_OAUTH_HMAC_KEY"
	oauthRotatedHMACKeysEnv    = "BANHMI_OAUTH_ROTATED_HMAC_KEYS"
	defaultConsentLifetime     = 10 * time.Minute
	minimumOAuthHMACSecretSize = 32
)

// Config contains the local OAuth endpoint and signing configuration.
type Config struct {
	Issuer               string
	Resource             string
	GlobalSecret         []byte
	RotatedGlobalSecrets [][]byte
	ConsentLifetime      time.Duration
}

// String keeps signing key material out of default formatted output.
func (cfg Config) String() string {
	return fmt.Sprintf("oauthserver.Config{Issuer:%q Resource:%q GlobalSecret:<redacted> RotatedGlobalSecrets:<redacted> ConsentLifetime:%s}", cfg.Issuer, cfg.Resource, cfg.ConsentLifetime)
}

// GoString keeps signing key material out of Go-syntax formatted output.
func (cfg Config) GoString() string {
	return cfg.String()
}

// LoadConfig reads OAuth signing keys from the process environment and derives
// the issuer and protected resource from the canonical public origin.
func LoadConfig(origin *url.URL) (Config, error) {
	if err := validateIssuerURL(origin); err != nil {
		return Config{}, fmt.Errorf("public origin is invalid: %w", err)
	}
	primary, err := decodeHMACKey(oauthHMACKeyEnv, os.Getenv(oauthHMACKeyEnv))
	if err != nil {
		return Config{}, err
	}
	rotated, err := decodeRotatedHMACKeys(os.Getenv(oauthRotatedHMACKeysEnv))
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		Issuer:               origin.String(),
		Resource:             origin.String() + "/mcp",
		GlobalSecret:         primary,
		RotatedGlobalSecrets: rotated,
		ConsentLifetime:      defaultConsentLifetime,
	}
	if err := validateServerConfig(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validateServerConfig(cfg Config) error {
	issuer, err := url.Parse(cfg.Issuer)
	if err != nil || validateIssuerURL(issuer) != nil || issuer.String() != cfg.Issuer {
		return errors.New("OAuth issuer is invalid")
	}
	if cfg.Resource != cfg.Issuer+"/mcp" {
		return errors.New("OAuth resource must equal the issuer origin plus /mcp")
	}
	if err := validateResource(cfg.Resource); err != nil {
		return fmt.Errorf("OAuth resource is invalid: %w", err)
	}
	if len(cfg.GlobalSecret) < minimumOAuthHMACSecretSize {
		return fmt.Errorf("%s must decode to at least %d bytes", oauthHMACKeyEnv, minimumOAuthHMACSecretSize)
	}
	seen := map[string]struct{}{string(cfg.GlobalSecret): {}}
	for index, secret := range cfg.RotatedGlobalSecrets {
		if len(secret) < minimumOAuthHMACSecretSize {
			return fmt.Errorf("%s entry %d must decode to at least %d bytes", oauthRotatedHMACKeysEnv, index, minimumOAuthHMACSecretSize)
		}
		if _, exists := seen[string(secret)]; exists {
			return fmt.Errorf("%s entry %d is duplicated", oauthRotatedHMACKeysEnv, index)
		}
		seen[string(secret)] = struct{}{}
	}
	if cfg.ConsentLifetime == 0 {
		cfg.ConsentLifetime = defaultConsentLifetime
	}
	if cfg.ConsentLifetime != defaultConsentLifetime {
		return errors.New("OAuth consent lifetime must be 10 minutes")
	}
	return nil
}

func validateIssuerURL(origin *url.URL) error {
	if origin == nil || origin.Scheme != "https" || origin.Host == "" || origin.Opaque != "" || origin.User != nil {
		return errors.New("origin must be an absolute HTTPS URL without userinfo")
	}
	if origin.Path != "" || origin.RawPath != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return errors.New("origin must not contain a path, query, or fragment")
	}
	if err := validateURLAuthority(origin); err != nil {
		return err
	}
	return nil
}

func decodeHMACKey(field, encoded string) ([]byte, error) {
	if strings.TrimSpace(encoded) == "" {
		return nil, fmt.Errorf("%s is required", field)
	}
	decoded, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("%s must be base64", field)
	}
	if len(decoded) < minimumOAuthHMACSecretSize {
		return nil, fmt.Errorf("%s must decode to at least %d bytes", field, minimumOAuthHMACSecretSize)
	}
	return decoded, nil
}

func decodeRotatedHMACKeys(raw string) ([][]byte, error) {
	if strings.TrimSpace(raw) == "" {
		return [][]byte{}, nil
	}
	var encoded []string
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&encoded); err != nil {
		return nil, fmt.Errorf("%s must be a JSON array of base64 strings", oauthRotatedHMACKeysEnv)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("%s must contain one JSON value", oauthRotatedHMACKeysEnv)
	}
	if encoded == nil {
		return nil, fmt.Errorf("%s must be a JSON array", oauthRotatedHMACKeysEnv)
	}
	keys := make([][]byte, len(encoded))
	for index, value := range encoded {
		key, err := decodeHMACKey(fmt.Sprintf("%s entry %d", oauthRotatedHMACKeysEnv, index), value)
		if err != nil {
			return nil, err
		}
		keys[index] = key
	}
	return keys, nil
}
