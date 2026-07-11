package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// Tenant context headers Core injects into proxied requests after resolving the
// device token + acting user through Identity.
const (
	HeaderTenantID    = "X-ApiCoreX-Tenant-ID"
	HeaderTenantSlug  = "X-ApiCoreX-Tenant-Slug"
	HeaderSchema      = "X-ApiCoreX-Schema"
	HeaderBranchID    = "X-ApiCoreX-Branch-ID"
	HeaderBranchSlug  = "X-ApiCoreX-Branch-Slug"
	HeaderUserID      = "X-ApiCoreX-User-ID"
	HeaderUserType    = "X-ApiCoreX-User-Type"
	HeaderRoles       = "X-ApiCoreX-Roles"
	HeaderPermissions = "X-ApiCoreX-Permissions"
	HeaderRequestID   = "X-ApiCoreX-Request-ID"
	// HeaderTokenHash carries the sha256 of the bearer device token so Identity's
	// logout / branch-switch can act on the exact token row without ever seeing
	// the raw token.
	HeaderTokenHash = "X-ApiCoreX-Token-Hash"
)

var apicorexHeaders = []string{
	HeaderTenantID, HeaderTenantSlug, HeaderSchema,
	HeaderBranchID, HeaderBranchSlug,
	HeaderUserID, HeaderUserType, HeaderRoles, HeaderPermissions, HeaderRequestID,
	HeaderTokenHash,
}

// StripSpoofedHeaders removes any client-supplied X-ApiCoreX-* headers so clients
// cannot impersonate a tenant/user. Runs before auth, on every request. Note:
// X-Acting-User is NOT stripped — it is legitimate client input, validated and
// consumed during auth.
func StripSpoofedHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, h := range apicorexHeaders {
			c.Request.Header.Del(h)
		}
		c.Next()
	}
}

// InjectTenantHeaders sets trusted X-ApiCoreX-* headers from the resolved
// identity onto the request, so the proxied plugin receives tenant context.
// User-ID is the ACTING user. The client's X-Acting-User header is deleted
// after consumption — plugins only ever see the trusted headers. No-op when
// the request is unauthenticated (public routes).
func InjectTenantHeaders(c *gin.Context) {
	id := IdentityFrom(c)
	if id == nil {
		return
	}
	h := c.Request.Header
	h.Del(HeaderActingUser)
	h.Set(HeaderTenantID, id.TenantID)
	h.Set(HeaderTenantSlug, id.TenantSlug)
	h.Set(HeaderSchema, id.SchemaName)
	h.Set(HeaderBranchID, id.BranchID)
	h.Set(HeaderBranchSlug, id.BranchSlug)
	h.Set(HeaderUserID, id.UserID)
	h.Set(HeaderUserType, id.UserType)
	h.Set(HeaderRoles, strings.Join(id.Roles, ","))
	h.Set(HeaderPermissions, strings.Join(id.Permissions, ","))
	h.Set(HeaderTokenHash, id.TokenHash)
	if rid := c.GetHeader("X-Request-ID"); rid != "" {
		h.Set(HeaderRequestID, rid)
	}
}
