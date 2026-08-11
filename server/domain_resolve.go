package server

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/auth"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
)

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

		host := c.Request.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		resolved, err := resolver.Resolve(c.Request.Context(), host)
		if err != nil {
			c.Next() // not a claimed domain, or Identity unreachable — 404 as before
			return
		}
		prefix, ok := disp.FindDomainSurface(resolved.Surface)
		if !ok {
			c.Next()
			return
		}

		// TrimSuffix so a bare root path ("/") contributes nothing beyond the
		// slug itself: prefix "/site" + "/" + slug + "" = "/site/<slug>",
		// matching the plugin's registered pattern exactly. Any other path
		// ("/login") is appended unchanged.
		c.Request.URL.Path = prefix + "/" + resolved.TenantSlug + strings.TrimSuffix(path, "/")
		c.Request.RequestURI = c.Request.URL.RequestURI()
		c.Next()
	}
}
