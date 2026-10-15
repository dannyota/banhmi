package db

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateConnectionURI rejects ambiguous userinfo before a driver can interpret
// a password fragment as a host. Keyword/value connection strings pass through.
// The configuration name must be a constant, never connection-string content.
func ValidateConnectionURI(name, value string) error {
	var authority string
	switch {
	case strings.HasPrefix(value, "postgres://"):
		authority = strings.TrimPrefix(value, "postgres://")
	case strings.HasPrefix(value, "postgresql://"):
		authority = strings.TrimPrefix(value, "postgresql://")
	default:
		return nil
	}
	if end := strings.IndexAny(authority, "/?"); end >= 0 {
		authority = authority[:end]
	}
	reject := func() error {
		return fmt.Errorf("%s: invalid connection URI; percent-encode @ in the password", name)
	}
	if strings.Count(authority, "@") > 1 {
		return reject()
	}
	// Decode the host separately: net/url rejects percent-encoded @ in a host
	// before returning a URL. Never propagate that parser error with its input.
	host := authority
	if end := strings.IndexByte(host, '@'); end >= 0 {
		host = host[end+1:]
	}
	if decoded, err := url.PathUnescape(host); err == nil && strings.Contains(decoded, "@") {
		return reject()
	}
	if parsed, err := url.Parse(value); err == nil && strings.Contains(parsed.Host, "@") {
		return reject()
	}
	return nil
}
