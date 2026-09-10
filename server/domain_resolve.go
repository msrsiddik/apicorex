package server

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/auth"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/middleware"
)

// resolvedTenantKey holds the tenant a session-scoped custom domain resolved to,
// between resolveCustomDomain (which knows the host) and requireHostTenant
// (which runs after auth and knows the session).
const resolvedTenantKey = "apicorex.host_tenant"

// resolveCustomDomain rewrites a request's path from a tenant's custom domain
// (which carries no path prefix at all — "portal.acme.com/login", not
// "gateway.example.com/school/portal/login") to the equivalent canonical,
// slug-prefixed path an ordinary plugin route already handles. Reusing that
// existing, already-verified slug-in-path lookup (rather than injecting a
// new trusted header) means a plugin needs no new trust boundary to support
// this — it sees exactly the URL shape it would if the guardian had typed
// the slug themselves. See manifest.DomainSurface and
// docs/multi-tenant-plan.md in schoolyze-server for the fuller design.
//
// A no-op whenever the path already routes normally, resolver is nil (no
// PLUGIN_API_KEY configured — the same condition that disables device-token
// auth), or the Host simply isn't a claimed custom domain. In every one of
// those cases the request falls through to whatever would have happened
// before this middleware existed — including an ordinary 404.
func resolveCustomDomain(disp *dispatcher.Dispatcher, resolver *auth.DomainResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		if resolver == nil {
			c.Next()
			return
		}
		method, path := c.Request.Method, c.Request.URL.Path
		if disp.IsRoutable(method, path) {
			c.Next()
			return
		}

		resolved, err := resolver.Resolve(c.Request.Context(), stripHostPort(c.Request.Host))
		if err != nil {
			c.Next() // not a claimed domain, or Identity unreachable — 404 as before
			return
		}
		declared, ok := disp.FindDomainSurface(resolved.Surface)
		if !ok {
			c.Next()
			return
		}

		if declared.SessionScoped {
			// No slug: this surface's tenant is the session's, and writing one
			// into the URL would invent a second, weaker answer to a question
			// already settled. The plugin is told the prefix was consumed so it
			// renders links without it, exactly as on a product host — and the
			// tenant this host resolved to is stashed for requireHostTenant
			// below, which runs once auth has produced a session to compare it
			// against.
			c.Request.URL.Path = declared.PathPrefix + strings.TrimSuffix(path, "/")
			c.Request.Header.Set(middleware.HeaderHostPrefix, declared.PathPrefix)
			c.Set(resolvedTenantKey, resolved.TenantID)
		} else {
			// TrimSuffix so a bare root path ("/") contributes nothing beyond
			// the slug itself: prefix "/site" + "/" + slug + "" = "/site/<slug>",
			// matching the plugin's registered pattern exactly. Any other path
			// ("/login") is appended unchanged.
			c.Request.URL.Path = declared.PathPrefix + "/" + resolved.TenantSlug + strings.TrimSuffix(path, "/")
		}
		c.Request.RequestURI = c.Request.URL.RequestURI()
		c.Next()
	}
}

// requireHostTenant refuses a session that belongs to a different institution
// than the hostname names.
//
// Only for a session-scoped surface (see manifest.DomainSurface): the ordinary
// custom-domain surfaces are public and carry their tenant in the path, so
// there is no session to disagree with. Here the URL deliberately carries no
// tenant at all, which means the check the slug used to perform has to happen
// somewhere — and Core is the only party holding both facts, since the plugin
// never learns which hostname the request arrived on beyond what Core tells it.
//
// A request with no identity passes: it is either a public asset or somebody
// not signed in, and both end at the login page rather than at another
// institution's records.
//
// Mounted after the auth split, which is the whole point — resolveCustomDomain
// runs before it and has no session to compare against yet.
func requireHostTenant() gin.HandlerFunc {
	return func(c *gin.Context) {
		want, ok := c.Get(resolvedTenantKey)
		if !ok {
			c.Next()
			return
		}
		id := middleware.IdentityFrom(c)
		if id == nil {
			c.Next()
			return
		}
		if id.TenantID != want.(string) {
			c.AbortWithStatusJSON(http.StatusForbidden,
				gin.H{"error": "this session belongs to a different institution than this domain"})
			return
		}
		c.Next()
	}
}
