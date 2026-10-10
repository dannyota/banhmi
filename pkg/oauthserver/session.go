package oauthserver

import (
	"strconv"
	"time"

	"github.com/ory/fosite"
)

// Session binds Fosite token expiry data to an internal account and resource.
type Session struct {
	*fosite.DefaultSession
	AccountID    int64  `json:"account_id"`
	AccountEpoch int64  `json:"account_epoch"`
	Resource     string `json:"resource"`
}

var _ fosite.Session = (*Session)(nil)

// NewSession returns an empty OAuth session for an internal account.
func NewSession(accountID, accountEpoch int64, resource string) *Session {
	return &Session{
		DefaultSession: &fosite.DefaultSession{
			ExpiresAt: make(map[fosite.TokenType]time.Time),
			Subject:   strconv.FormatInt(accountID, 10),
		},
		AccountID:    accountID,
		AccountEpoch: accountEpoch,
		Resource:     resource,
	}
}

func (s *Session) Clone() fosite.Session {
	if s == nil {
		return nil
	}
	defaultSession := s.DefaultSession
	if defaultSession == nil {
		defaultSession = &fosite.DefaultSession{}
	}
	clonedDefault, ok := defaultSession.Clone().(*fosite.DefaultSession)
	if !ok {
		return nil
	}
	clone := &Session{
		DefaultSession: clonedDefault,
		AccountID:    s.AccountID,
		AccountEpoch: s.AccountEpoch,
		Resource:     s.Resource,
	}
	return clone
}
