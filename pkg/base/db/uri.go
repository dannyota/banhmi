package db

import (
	"fmt"
	"net/url"
	"strings"
)

// ValidateConnectionURI rejects ambiguous userinfo before a driver can interpret
// a password fragment as a user or host. Unusual authorities must percent-encode
// @, ?, and # in the user and password. Keyword/value connection strings pass through.
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
	// Mirror pgx v5.11.0 pgconn/parse_url.go:75-80: only '/' stops the
	// userinfo lookahead, and the first '@' terminates userinfo. A '?' or '#'
	// before that '@' is consumed as userinfo, not as a query or fragment.
	if end := strings.IndexByte(authority, '/'); end >= 0 {
		authority = authority[:end]
	}
	reject := func() error {
		return fmt.Errorf("%s: invalid connection URI; percent-encode @, ?, and # in the user and password", name)
	}
	host := authority
	// Query overrides do not excuse ambiguous userinfo. Fail closed before
	// driver parsing, even when those overrides would hide stranded fragments.
	if i := strings.IndexAny(authority, "@/"); i >= 0 && authority[i] == '@' {
		userinfo := authority[:i]
		host = authority[i+1:]
		if strings.ContainsAny(userinfo, "?#") || strings.Contains(host, "@") {
			return reject()
		}
	}
	// Decode the host separately: net/url rejects percent-encoded @ in a host
	// before returning a URL. Never propagate that parser error with its input.
	// Keep query values out of this existing encoded-host check.
	if end := strings.IndexByte(host, '?'); end >= 0 {
		host = host[:end]
	}
	if decoded, err := url.PathUnescape(host); err == nil && strings.Contains(decoded, "@") {
		return reject()
	}
	if parsed, err := url.Parse(value); err == nil && strings.Contains(parsed.Host, "@") {
		return reject()
	}
	return nil
}
