// Package middleware holds Core's Gin middleware: device-token authentication
// (via Identity introspection) and tenant-context header handling. Together
// they ensure every proxied request carries trusted, un-spoofable tenant
// identity resolved fresh for the ACTING user.
package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/auth"
)

const identityKey = "identity"

// HeaderActingUser is CLIENT-supplied input: the PIN-unlocked user the device
// claims is operating it. It is validated (membership + status) during
// introspection, then consumed — plugins never see it, only the trusted
// X-ApiCoreX-User-ID that results.
const HeaderActingUser = "X-Acting-User"

// Auth returns middleware that requires a valid opaque device token
// (Bearer zdt_...). The token is hashed and resolved through Identity together
// with the optional X-Acting-User header; the resulting Identity is stored on
// the context for IdentityFrom. A nil introspector disables auth (dev only).
func Auth(introspector *auth.Introspector) gin.HandlerFunc {
	return func(c *gin.Context) {
		if introspector == nil {
			c.Next()
			return
		}
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}
		tokenStr := strings.TrimPrefix(header, "Bearer ")
		if !strings.HasPrefix(tokenStr, "zdt_") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		}
		id, err := introspector.Resolve(c.Request.Context(), auth.HashToken(tokenStr), c.GetHeader(HeaderActingUser))
		switch {
		case errors.Is(err, auth.ErrMembershipRevoked):
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "membership revoked"})
			return
		case errors.Is(err, auth.ErrInvalidToken):
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
			return
		case err != nil:
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "auth unavailable"})
			return
		}
		c.Set(identityKey, id)
		c.Next()
	}
}

// IdentityFrom returns the resolved identity stored by Auth, or nil if the
// request was not authenticated (e.g. a public route).
func IdentityFrom(c *gin.Context) *auth.Identity {
	v, _ := c.Get(identityKey)
	id, _ := v.(*auth.Identity)
	return id
}
