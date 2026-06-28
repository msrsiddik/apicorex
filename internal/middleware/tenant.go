package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// Tenant context headers Core injects into proxied requests after verifying the JWT.
const (
	HeaderTenantID   = "X-ApiCoreX-Tenant-ID"
	HeaderTenantSlug = "X-ApiCoreX-Tenant-Slug"
	HeaderSchema     = "X-ApiCoreX-Schema"
	HeaderUserID     = "X-ApiCoreX-User-ID"
	HeaderUserType   = "X-ApiCoreX-User-Type"
	HeaderRoles      = "X-ApiCoreX-Roles"
	HeaderRequestID  = "X-ApiCoreX-Request-ID"
)

var apicorexHeaders = []string{
	HeaderTenantID, HeaderTenantSlug, HeaderSchema,
	HeaderUserID, HeaderUserType, HeaderRoles, HeaderRequestID,
}

// StripSpoofedHeaders removes any client-supplied X-ApiCoreX-* headers so clients
// cannot impersonate a tenant/user. Runs before auth, on every request.
func StripSpoofedHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, h := range apicorexHeaders {
			c.Request.Header.Del(h)
		}
		c.Next()
	}
}

// InjectTenantHeaders sets trusted X-ApiCoreX-* headers from verified JWT claims
// onto the request, so the proxied plugin receives tenant context. No-op when
// the request is unauthenticated (public routes).
func InjectTenantHeaders(c *gin.Context) {
	claims := ClaimsFrom(c)
	if claims == nil {
		return
	}
	h := c.Request.Header
	h.Set(HeaderTenantID, claims.TenantID)
	h.Set(HeaderTenantSlug, claims.TenantSlug)
	h.Set(HeaderSchema, claims.SchemaName)
	h.Set(HeaderUserID, claims.Subject)
	h.Set(HeaderUserType, claims.UserType)
	h.Set(HeaderRoles, strings.Join(claims.Roles, ","))
	if rid := c.GetHeader("X-Request-ID"); rid != "" {
		h.Set(HeaderRequestID, rid)
	}
}
