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

// TokenCookie is where a browser-driven plugin UI keeps its device token.
//
// A server-rendered page cannot set an Authorization header on an ordinary
// navigation — a bookmark, a refresh, a link — so a plugin that serves HTML
// (rather than being called by an app) has no way to authenticate the very
// first request. Reading the same token from a cookie fixes that without
// changing anything downstream: it is hashed, introspected and turned into the
// same X-ApiCoreX-* headers as a bearer token.
//
// The header still wins when both are present, so apps are unaffected.
//
// Whoever sets this cookie must mark it HttpOnly and Secure, and must protect
// mutating requests against CSRF: unlike a bearer header, a cookie is attached
// by the browser automatically, so a cross-site form post would otherwise
// carry the user's credentials.
const TokenCookie = "apicorex_token"

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
		tokenStr, ok := deviceToken(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}
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

// OptionalAuth resolves whatever token is present and stores the identity for
// IdentityFrom, but never rejects the request — a missing, malformed, or
// invalid token just leaves the identity unset. For public routes.
//
// A public route serves both an unauthenticated visitor (a login page, a
// static asset) and, on the very same path, a signed-in one reading a
// cookie-based session (a server-rendered dashboard under that same public
// prefix — see TokenCookie): Auth would reject the first, and skipping auth
// entirely for public routes leaves the second permanently logged out, since
// InjectTenantHeaders has no identity to inject from. This is the middle
// ground: try to resolve, keep going either way.
func OptionalAuth(introspector *auth.Introspector) gin.HandlerFunc {
	return func(c *gin.Context) {
		if introspector == nil {
			c.Next()
			return
		}
		tokenStr, ok := deviceToken(c)
		if !ok || !strings.HasPrefix(tokenStr, "zdt_") {
			c.Next()
			return
		}
		id, err := introspector.Resolve(c.Request.Context(), auth.HashToken(tokenStr), c.GetHeader(HeaderActingUser))
		if err == nil {
			c.Set(identityKey, id)
		}
		c.Next()
	}
}

// deviceToken pulls the caller's device token from the Authorization header,
// falling back to the cookie a server-rendered plugin UI sets (see TokenCookie).
//
// The header takes precedence so an app that sends both is never surprised by a
// stale cookie left over from a browser session on the same host.
func deviceToken(c *gin.Context) (string, bool) {
	if h := c.GetHeader("Authorization"); strings.HasPrefix(h, "Bearer ") {
		if t := strings.TrimPrefix(h, "Bearer "); t != "" {
			return t, true
		}
	}
	if t, err := c.Cookie(TokenCookie); err == nil && t != "" {
		return t, true
	}
	return "", false
}

// IdentityFrom returns the resolved identity stored by Auth, or nil if the
// request was not authenticated (e.g. a public route).
func IdentityFrom(c *gin.Context) *auth.Identity {
	v, _ := c.Get(identityKey)
	id, _ := v.(*auth.Identity)
	return id
}
