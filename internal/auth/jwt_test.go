package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func signToken(t *testing.T, secret string, claims Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func TestVerifier_ValidToken(t *testing.T) {
	const secret = "test-secret"
	v := NewVerifier(secret)

	want := Claims{
		TenantID:   "t_acme",
		SchemaName: "tenant_acme",
		Roles:      []string{"owner"},
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "u1",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	}
	got, err := v.Verify(signToken(t, secret, want))
	if err != nil {
		t.Fatalf("verify valid token: %v", err)
	}
	if got.TenantID != "t_acme" || got.Subject != "u1" || got.SchemaName != "tenant_acme" {
		t.Fatalf("claims mismatch: %+v", got)
	}
}

func TestVerifier_WrongSecret(t *testing.T) {
	v := NewVerifier("right-secret")
	token := signToken(t, "wrong-secret", Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	})
	if _, err := v.Verify(token); err == nil {
		t.Fatal("token signed with a different secret must be rejected")
	}
}

func TestVerifier_ExpiredToken(t *testing.T) {
	const secret = "test-secret"
	v := NewVerifier(secret)
	token := signToken(t, secret, Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)), // already expired
		},
	})
	if _, err := v.Verify(token); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestVerifier_GarbageToken(t *testing.T) {
	v := NewVerifier("test-secret")
	if _, err := v.Verify("not.a.jwt"); err == nil {
		t.Fatal("malformed token must be rejected")
	}
}
