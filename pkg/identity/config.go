package identity

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	publicOriginEnv       = "BANHMI_PUBLIC_ORIGIN"
	authDatabaseURLEnv    = "BANHMI_AUTH_DATABASE_URL"
	googleClientIDEnv     = "BANHMI_GOOGLE_CLIENT_ID"
	googleClientSecretEnv = "BANHMI_GOOGLE_CLIENT_SECRET"
	transactionKeyEnv     = "BANHMI_AUTH_TRANSACTION_KEY"
)

// Config contains the local authentication configuration needed by the server.
// Secret fields are loaded only from the process environment.
type Config struct {
	PublicOrigin       *url.URL
	AuthDatabaseURL    string
	GoogleClientID     string
	GoogleClientSecret string
	TransactionKey     [32]byte
}

// LoadConfig reads and validates authentication configuration from the process environment.
func LoadConfig() (Config, error) {
	var cfg Config

	origin, err := requiredEnv(publicOriginEnv)
	if err != nil {
		return cfg, err
	}
	cfg.PublicOrigin, err = parsePublicOrigin(origin)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", publicOriginEnv, err)
	}

	if cfg.AuthDatabaseURL, err = requiredEnv(authDatabaseURLEnv); err != nil {
		return Config{}, err
	}
	if cfg.GoogleClientID, err = requiredEnv(googleClientIDEnv); err != nil {
		return Config{}, err
	}
	if cfg.GoogleClientSecret, err = requiredEnv(googleClientSecretEnv); err != nil {
		return Config{}, err
	}

	encodedKey, err := requiredEnv(transactionKeyEnv)
	if err != nil {
		return Config{}, err
	}
	key, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(key) != len(cfg.TransactionKey) {
		return Config{}, fmt.Errorf("%s must be base64 for exactly 32 bytes", transactionKeyEnv)
	}
	copy(cfg.TransactionKey[:], key)

	return cfg, nil
}

func requiredEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func parsePublicOrigin(raw string) (*url.URL, error) {
	if strings.ContainsAny(raw, "?#") {
		return nil, fmt.Errorf("must not contain a query or fragment delimiter")
	}
	origin, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("must be an absolute HTTPS origin")
	}
	if origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, fmt.Errorf("must be an absolute HTTPS origin without path, query, fragment, or userinfo")
	}
	if strings.HasSuffix(origin.Host, ":") {
		return nil, fmt.Errorf("must not contain an empty port")
	}
	if port := origin.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, fmt.Errorf("port must be between 1 and 65535")
		}
	}
	return origin, nil
}
