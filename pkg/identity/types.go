// Package identity persists Google identities and website sessions.
package identity

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrUnauthenticated reports a missing, expired, consumed, or revoked credential.
	ErrUnauthenticated = errors.New("unauthenticated")
	// ErrBlocked reports an authenticated account whose current status is blocked.
	ErrBlocked = errors.New("account blocked")
)

// Profile is the verified identity returned by Google OpenID Connect.
type Profile struct {
	Issuer      string
	Subject     string
	Email       string
	DisplayName string
}

// Principal is the current account and website-session authorization state.
type Principal struct {
	AccountID          int64
	Email              string
	DisplayName        string
	AuthorizationEpoch int64
	SessionHash        []byte
}

// LoginTransaction contains the one-use state needed to finish a Google login.
type LoginTransaction struct {
	StateHash              []byte
	BrowserHash            []byte
	NonceHash              []byte
	PKCEVerifierCiphertext []byte
	ReturnPath             string
	PendingConsentHash     []byte
	ExpiresAt              time.Time
}

// Store persists one-use login transactions, accounts, and website sessions.
type Store interface {
	CreateLogin(context.Context, LoginTransaction) error
	ConsumeLogin(context.Context, []byte, []byte, time.Time) (LoginTransaction, error)
	CompleteLogin(context.Context, Profile, []byte, time.Time, time.Time) (Principal, error)
	Session(context.Context, []byte, time.Time) (Principal, error)
	RevokeSession(context.Context, []byte, time.Time) error
}
