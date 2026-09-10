package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// Tenant context headers Core injects into proxied requests after resolving the
// device token + acting user through Identity.
const (
	HeaderTenantID   = "X-ApiCoreX-Tenant-ID"
	HeaderTenantSlug = "X-ApiCoreX-Tenant-Slug"
	// HeaderTenantName and HeaderBranchName are display names for a panel's
	// header; the slugs beside them identify, and are not meant to be read.
	HeaderTenantName = "X-ApiCoreX-Tenant-Name"
	HeaderSchema     = "X-ApiCoreX-Schema"
	// HeaderTenantSchema carries the tenant's BASE schema (tenant_<slug>),
	// alongside HeaderSchema's per-plugin one.
	//
	// A plugin needs it to call another plugin. Schema-per-plugin means the
	// caller's own schema name is useless to the callee — schoolyze telling
	// accounting "tenant_acme__schoolyze" names a schema accounting cannot read
	// and has no tables in. What travels between plugins has to be the tenant,
	// and the callee derives its own schema from it.
	HeaderTenantSchema = "X-ApiCoreX-Tenant-Schema"
	// HeaderNav carries the suite's merged sidebar, base64-encoded JSON.
	//
	// Assembled by Core because it is the only party that knows which plugins a
	// tenant has and what this user may open; rendered by the plugin because
	// Core has no design language and should not acquire one. Absent when there
	// is nothing to send, or when it would not fit — a plugin that gets no
	// header falls back to its own menu.
	HeaderNav        = "X-ApiCoreX-Nav"
	HeaderBranchID   = "X-ApiCoreX-Branch-ID"
	HeaderBranchSlug = "X-ApiCoreX-Branch-Slug"
	HeaderBranchName = "X-ApiCoreX-Branch-Name"
	HeaderUserID     = "X-ApiCoreX-User-ID"
	// HeaderUserName is the acting user's name, for a panel's header. Without
	// it a panel can only show the id, which is what every panel did.
	HeaderUserName    = "X-ApiCoreX-User-Name"
	HeaderUserType    = "X-ApiCoreX-User-Type"
	HeaderRoles       = "X-ApiCoreX-Roles"
	HeaderPermissions = "X-ApiCoreX-Permissions"
	// HeaderFeatures carries the tenant's enabled plugin modules, qualified as
	// "plugin:key". Resolved per TENANT, not per user: permissions say whether
	// this person may act, features say whether the institution has the module
	// at all. Identity decides from the tenant's plan and any override.
	HeaderFeatures  = "X-ApiCoreX-Features"
	HeaderRequestID = "X-ApiCoreX-Request-ID"
	// HeaderTokenHash carries the sha256 of the bearer device token so Identity's
	// logout / branch-switch can act on the exact token row without ever seeing
	// the raw token.
	HeaderTokenHash = "X-ApiCoreX-Token-Hash"
	// HeaderForwardedHost carries the hostname the browser actually asked for.
	//
	// A plugin cannot otherwise know it: the proxy director overwrites Host with
	// the plugin's own address so the outbound request addresses the plugin
	// (see dispatcher.ProxyFor), and nothing else carried it. That left every
	// host-based decision in a plugin reading the plugin's own address —
	// Schoolyze's subdomain-to-institution resolution matched nothing, in every
	// deployment, silently, because its slug-in-path fallback always works.
	//
	// Set from Core's own Request.Host, never from an inbound X-Forwarded-Host:
	// that header is client-writable and this one is trusted downstream. A Core
	// behind another proxy therefore needs that proxy to preserve Host
	// (`proxy_set_header Host $host`); one that does not makes host-based
	// resolution fall back rather than resolve to something forged.
	HeaderForwardedHost = "X-ApiCoreX-Forwarded-Host"
	// HeaderHostPrefix names the path prefix Core consumed from the URL because
	// the request arrived on a host dedicated to one plugin's surface — see
	// PRODUCT_HOSTS and server.productHostRewrite.
	//
	// It exists so the plugin can render links that match the URL the browser
	// is on. A path rewrite alone cannot carry that: the same page must emit
	// "/students" on panel.example.com and "/school/students" on the gateway,
	// and only the plugin renders links. Absent means nothing was consumed and
	// the plugin's own prefix is still in the URL — which is the ordinary case
	// and the one every existing deployment stays in.
	HeaderHostPrefix = "X-ApiCoreX-Host-Prefix"
)

var apicorexHeaders = []string{
	HeaderTenantID, HeaderTenantSlug, HeaderSchema, HeaderTenantSchema, HeaderNav,
	HeaderBranchID, HeaderBranchSlug,
	HeaderTenantName, HeaderBranchName,
	HeaderUserID, HeaderUserName, HeaderUserType, HeaderRoles, HeaderPermissions, HeaderFeatures,
	HeaderRequestID, HeaderTokenHash, HeaderForwardedHost, HeaderHostPrefix,
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

// InjectForwardedHost records the hostname the browser asked for, so the plugin
// can read it after the director has replaced Host with the plugin's address.
//
// Separate from InjectTenantHeaders, and called unconditionally, because that
// one returns early on a public route where no identity was resolved — and a
// public route is precisely where this is needed. The guardian Portal is
// public at the gateway: it is the surface that resolves an institution from
// its hostname, and it is the surface that would never have received it.
func InjectForwardedHost(c *gin.Context) {
	if host := c.Request.Host; host != "" {
		c.Request.Header.Set(HeaderForwardedHost, host)
	}
}

// InjectTenantHeaders sets trusted X-ApiCoreX-* headers from the resolved
// identity onto the request, so the proxied plugin receives tenant context.
// User-ID is the ACTING user. The client's X-Acting-User header is deleted
// after consumption — plugins only ever see the trusted headers. No-op when
// the request is unauthenticated (public routes).
//
// schema is the schema this particular plugin owns, which the caller derives
// from the plugin's manifest — see dispatcher.Dispatch. It is passed in rather
// than read from the identity because the identity carries the tenant's base
// schema and is cached across plugins, while this header is per-plugin: the same
// token proxied to two plugins must name two different schemas.
func InjectTenantHeaders(c *gin.Context, schema, nav string) {
	id := IdentityFrom(c)
	if id == nil {
		return
	}
	h := c.Request.Header
	h.Del(HeaderActingUser)
	h.Set(HeaderTenantID, id.TenantID)
	h.Set(HeaderTenantSlug, id.TenantSlug)
	h.Set(HeaderSchema, schema)
	// The tenant's own schema, for plugin-to-plugin calls — see the header's
	// definition above.
	h.Set(HeaderTenantSchema, id.SchemaName)
	if nav != "" {
		h.Set(HeaderNav, nav)
	}
	h.Set(HeaderBranchID, id.BranchID)
	h.Set(HeaderBranchSlug, id.BranchSlug)
	if id.TenantName != "" {
		h.Set(HeaderTenantName, id.TenantName)
	}
	if id.BranchName != "" {
		h.Set(HeaderBranchName, id.BranchName)
	}
	h.Set(HeaderUserID, id.UserID)
	if id.FullName != "" {
		h.Set(HeaderUserName, id.FullName)
	}
	h.Set(HeaderUserType, id.UserType)
	h.Set(HeaderRoles, strings.Join(id.Roles, ","))
	h.Set(HeaderPermissions, strings.Join(id.Permissions, ","))
	h.Set(HeaderFeatures, strings.Join(id.Features, ","))
	h.Set(HeaderTokenHash, id.TokenHash)
	if rid := c.GetHeader("X-Request-ID"); rid != "" {
		h.Set(HeaderRequestID, rid)
	}
}
