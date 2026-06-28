// Package middleware holds Core's Gin middleware: JWT authentication (with an
// optional logout denylist) and tenant-context header handling. Together they
// ensure every proxied request carries trusted, un-spoofable tenant identity.
package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/auth"
)

const claimsKey = "claims"

// Auth returns middleware that requires a valid Bearer access token. It rejects
// missing/invalid tokens and, when a denylist is configured, revoked (logged-out)
// ones. On success the claims are stored on the context for ClaimsFrom. A nil
// verifier disables auth (dev only).
func Auth(verifier *auth.Verifier, denylist *auth.Denylist) gin.HandlerFunc {
	return func(c *gin.Context) {
		if verifier == nil {
			c.Next()
			return
		}
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}
		tokenStr := strings.TrimPrefix(header, "Bearer ")
		claims, err := verifier.Verify(tokenStr)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		// logout denylist check (revoked access tokens)
		if denylist.IsRevoked(c.Request.Context(), claims.ID) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "token revoked"})
			return
		}
		c.Set(claimsKey, claims)
		c.Next()
	}
}

// ClaimsFrom returns the verified claims stored by Auth, or nil if the request
// was not authenticated (e.g. a public route).
func ClaimsFrom(c *gin.Context) *auth.Claims {
	v, _ := c.Get(claimsKey)
	claims, _ := v.(*auth.Claims)
	return claims
}
