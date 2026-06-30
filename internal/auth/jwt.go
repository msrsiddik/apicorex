// Package auth verifies the access tokens issued by the Identity plugin and
// (optionally) checks a Redis logout denylist. Core only verifies tokens; it
// never issues them, so it needs only the shared HMAC secret.
package auth

import (
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the set of tenant/user fields Core reads from a verified access
// token and injects into proxied requests as X-ApiCoreX-* headers. The standard
// registered claims (subject, expiry, jti) are embedded.
type Claims struct {
	TenantID    string   `json:"tenant_id"`
	TenantSlug  string   `json:"tenant_slug"`
	SchemaName  string   `json:"schema_name"`
	BranchID    string   `json:"branch_id,omitempty"`
	BranchSlug  string   `json:"branch_slug,omitempty"`
	UserType    string   `json:"user_type"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions,omitempty"`
	jwt.RegisteredClaims
}

// Verifier validates HS256 access tokens with a shared secret.
type Verifier struct {
	secret []byte
}

// NewVerifier returns a Verifier using the given HMAC secret (JWT_SECRET).
func NewVerifier(secret string) *Verifier {
	return &Verifier{secret: []byte(secret)}
}

// Verify parses and validates a token string, returning its claims. It rejects
// tokens not signed with HMAC or whose signature/expiry is invalid.
func (v *Verifier) Verify(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return v.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token claims")
	}
	return claims, nil
}
