package oauthserver

import (
	"testing"
	"time"

	"github.com/ory/fosite"
)

func TestNewSessionUsesInternalAccountSubject(t *testing.T) {
	session := NewSession(42, 7, testResource)
	if session.GetSubject() != "42" {
		t.Fatalf("GetSubject() = %q, want 42", session.GetSubject())
	}
	if session.GetUsername() != "" {
		t.Fatalf("GetUsername() = %q, want empty", session.GetUsername())
	}
	if session.AccountID != 42 || session.AccountEpoch != 7 || session.Resource != testResource {
		t.Fatalf("session identity = (%d, %d, %q), want (42, 7, %q)", session.AccountID, session.AccountEpoch, session.Resource, testResource)
	}
}

func TestSessionCloneDeepCopiesExpirationMap(t *testing.T) {
	expires := time.Date(2026, time.October, 11, 10, 0, 0, 0, time.UTC)
	session := NewSession(42, 7, testResource)
	session.SetExpiresAt(fosite.AccessToken, expires)

	clone, ok := session.Clone().(*Session)
	if !ok {
		t.Fatalf("Clone() type = %T, want *Session", session.Clone())
	}
	clone.SetExpiresAt(fosite.AccessToken, expires.Add(time.Hour))
	clone.AccountEpoch = 8

	if got := session.GetExpiresAt(fosite.AccessToken); !got.Equal(expires) {
		t.Fatalf("original expiry = %v, want %v", got, expires)
	}
	if session.AccountEpoch != 7 {
		t.Fatalf("original account epoch = %d, want 7", session.AccountEpoch)
	}
}

func TestSessionCloneDeepCopiesExtraClaims(t *testing.T) {
	session := NewSession(42, 7, testResource)
	session.DefaultSession.Extra = map[string]any{
		"top": "original",
		"nested": map[string]any{
			"value": "original",
		},
	}

	clone := session.Clone().(*Session)
	clone.DefaultSession.Extra["top"] = "changed"
	clone.DefaultSession.Extra["nested"].(map[string]any)["value"] = "changed"

	if got := session.DefaultSession.Extra["top"]; got != "original" {
		t.Fatalf("original top claim = %v, want original", got)
	}
	if got := session.DefaultSession.Extra["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("original nested claim = %v, want original", got)
	}
}
