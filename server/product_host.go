package server

import (
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/middleware"
	"github.com/msrsiddik/apicorex/internal/protection"
)

// A product host is a hostname dedicated to one plugin's surface:
// "panel.example.com" serving what "<gateway>/school" serves, with no prefix in
// the URL. Unlike a custom domain (resolveCustomDomain) it names a *product*,
// not a tenant — one mapping for the whole deployment, no call to Identity, and
// no slug injected into the path.
//
// See docs/panel-domain-plan.md in schoolyze-server for the design this
// implements and the open questions it does not.

// parseProductHosts reads the PRODUCT_HOSTS setting:
//
//	panel.example.com=/school,gl.example.com=/accounting
//
// Configuration rather than something a plugin declares in its manifest: a
// plugin naming a public hostname would be claiming DNS it does not own, and it
// cannot know the deployment's domain in any case.
//
// Every malformed entry is an error rather than a skip. A silently dropped
// mapping is a hostname that serves 404s for a reason nothing reports, and the
// operator who typed it has no way to tell it from a DNS problem.
func parseProductHosts(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		host, prefix, ok := strings.Cut(entry, "=")
		if !ok {
			return nil, fmt.Errorf("entry %q: want host=/prefix", entry)
		}
		host = strings.TrimSpace(host)
		prefix = strings.TrimSpace(prefix)
		if host == "" {
			return nil, fmt.Errorf("entry %q: empty host", entry)
		}
		// Checked before the port is stripped: SplitHostPort reads
		// "https://panel.example.com" as host "https" port "//panel.example.com"
		// and hands back something that then looks like a valid bare hostname.
		if strings.Contains(host, "/") {
			return nil, fmt.Errorf("entry %q: host must be a bare hostname, not a URL", entry)
		}
		host = strings.ToLower(stripHostPort(host))
		if strings.Contains(host, ":") {
			return nil, fmt.Errorf("entry %q: host must be a bare hostname, not a URL", entry)
		}
		if prefix == "" || prefix == "/" {
			return nil, fmt.Errorf("entry %q: prefix must name a plugin's path, e.g. /school", entry)
		}
		prefix = "/" + strings.Trim(prefix, "/")
		// A prefix inside a reserved namespace would map a hostname onto paths
		// the firewall blocks — a host that can only ever answer 403.
		if protection.IsRouteBlocked(prefix + "/") {
			return nil, fmt.Errorf("entry %q: %s is a reserved prefix", entry, prefix)
		}
		if existing, dup := out[host]; dup {
			return nil, fmt.Errorf("host %q mapped twice (%s and %s)", host, existing, prefix)
		}
		out[host] = prefix
	}
	return out, nil
}

// productHostNames lists the configured hosts, sorted, for the startup log.
func productHostNames(hosts map[string]string) []string {
	names := make([]string, 0, len(hosts))
	for h := range hosts {
		names = append(names, h)
	}
	sort.Strings(names)
	return names
}

// productHostRewrite puts a product host's missing path prefix back before the
// request is routed.
//
// Deliberately the same shape as resolveCustomDomain, and for the same reason:
// rewriting only what nothing else already serves is what keeps the suite's
// shared paths working on a product host. "/login" and "/logout" are Identity's,
// "/accounting/..." is another plugin's, "/_core/..." is the control plane's —
// all of them route already, so all of them fall through untouched. Only a path
// no plugin claims is assumed to be the product's own.
//
// The consequence, worth knowing before adding a route: on a product host the
// gateway's root namespace shadows the plugin's, silently, because IsRoutable
// runs first.
func productHostRewrite(disp *dispatcher.Dispatcher, hosts map[string]string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(hosts) == 0 {
			c.Next()
			return
		}
		method, path := c.Request.Method, c.Request.URL.Path
		if disp.IsRoutable(method, path) {
			c.Next()
			return
		}
		// A blocked path stays blocked rather than being rewritten out of the
		// reserved namespace it named.
		if protection.IsRouteBlocked(path) {
			c.Next()
			return
		}
		prefix, ok := hosts[strings.ToLower(stripHostPort(c.Request.Host))]
		if !ok {
			c.Next() // not a product host — 404 exactly as before
			return
		}

		// TrimSuffix so a bare "/" contributes nothing beyond the prefix itself:
		// "/school" + "" matches the registered pattern, "/school/" would not.
		c.Request.URL.Path = prefix + strings.TrimSuffix(path, "/")
		c.Request.RequestURI = c.Request.URL.RequestURI()
		// Told to the plugin, not merely done to the path: the plugin has to
		// render links without this prefix, and no rewritten path can say that.
		// Safe to set here — StripSpoofedHeaders has already removed any copy
		// the client sent.
		c.Request.Header.Set(middleware.HeaderHostPrefix, prefix)
		c.Next()
	}
}

// stripHostPort drops the ":port" from a Host header, leaving the hostname.
func stripHostPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
