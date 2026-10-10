package identity

import (
	"context"
	"testing"
	"time"
)

func TestCreateLoginRejectsIncompleteTransactionBeforeDatabaseAccess(t *testing.T) {
	store := NewPostgresStore(nil)
	tx := LoginTransaction{
		StateHash:              []byte("state"),
		BrowserHash:            []byte("browser"),
		NonceHash:              []byte("nonce"),
		PKCEVerifierCiphertext: []byte("ciphertext"),
		ReturnPath:             "/app",
		ExpiresAt:              time.Now().Add(time.Minute),
	}

	tests := []struct {
		name   string
		mutate func(*LoginTransaction)
	}{
		{name: "state", mutate: func(tx *LoginTransaction) { tx.StateHash = nil }},
		{name: "browser", mutate: func(tx *LoginTransaction) { tx.BrowserHash = nil }},
		{name: "nonce", mutate: func(tx *LoginTransaction) { tx.NonceHash = nil }},
		{name: "PKCE verifier", mutate: func(tx *LoginTransaction) { tx.PKCEVerifierCiphertext = nil }},
		{name: "return path", mutate: func(tx *LoginTransaction) { tx.ReturnPath = "https://bad.example" }},
		{name: "expiry", mutate: func(tx *LoginTransaction) { tx.ExpiresAt = time.Time{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := tx
			test.mutate(&candidate)
			if err := store.CreateLogin(context.Background(), candidate); err == nil {
				t.Fatal("CreateLogin() error = nil, want validation error")
			}
		})
	}
}

func TestCompleteLoginRejectsIncompleteProfileBeforeDatabaseAccess(t *testing.T) {
	store := NewPostgresStore(nil)
	now := time.Now()
	profile := Profile{
		Issuer:  "https://accounts.google.com",
		Subject: "subject",
		Email:   "person@example.com",
	}

	tests := []struct {
		name   string
		mutate func(*Profile)
	}{
		{name: "issuer", mutate: func(profile *Profile) { profile.Issuer = "" }},
		{name: "subject", mutate: func(profile *Profile) { profile.Subject = "" }},
		{name: "email", mutate: func(profile *Profile) { profile.Email = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := profile
			test.mutate(&candidate)
			_, err := store.CompleteLogin(
				context.Background(), candidate, []byte("session"), now, now.Add(time.Hour),
			)
			if err == nil {
				t.Fatal("CompleteLogin() error = nil, want validation error")
			}
		})
	}
}
