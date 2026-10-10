package identity

import (
	"bytes"
	"testing"
)

func TestSealRoundTripAndPurposeBinding(t *testing.T) {
	m := testManager(t, newMemoryStore(), nil)
	plain := []byte("short-lived secret")

	sealed, err := m.Seal("login-pkce", plain)
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	opened, err := m.Open("login-pkce", sealed)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("Open() = %q, want %q", opened, plain)
	}
	if _, err := m.Open("pending-consent", sealed); err == nil {
		t.Fatal("Open() with another purpose error = nil")
	}
}

func TestSealUsesIndependentNonces(t *testing.T) {
	m := testManager(t, newMemoryStore(), nil)
	first, err := m.Seal("login-pkce", []byte("same"))
	if err != nil {
		t.Fatalf("first Seal() error = %v", err)
	}
	second, err := m.Seal("login-pkce", []byte("same"))
	if err != nil {
		t.Fatalf("second Seal() error = %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("Seal() reused an AES-GCM nonce")
	}
}
