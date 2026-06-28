package controlplane

import (
	"testing"
	"time"
)

func TestTokenSigner_RoundTrip(t *testing.T) {
	s := newTokenSigner("secret", time.Hour)
	token := s.issue("billing-123")

	got, err := s.verify(token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got != "billing-123" {
		t.Fatalf("plugin id = %q, want billing-123", got)
	}
}

func TestTokenSigner_WrongSecret(t *testing.T) {
	a := newTokenSigner("secret-a", time.Hour)
	b := newTokenSigner("secret-b", time.Hour)

	token := a.issue("p1")
	if _, err := b.verify(token); err == nil {
		t.Fatal("token must not verify under a different secret")
	}
}

func TestTokenSigner_Expired(t *testing.T) {
	s := newTokenSigner("secret", -time.Minute) // already expired
	token := s.issue("p1")
	if _, err := s.verify(token); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestTokenSigner_Tampered(t *testing.T) {
	s := newTokenSigner("secret", time.Hour)
	token := s.issue("p1")
	if _, err := s.verify(token + "x"); err == nil {
		t.Fatal("tampered token must be rejected")
	}
}
