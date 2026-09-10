package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/auth"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/middleware"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
)

// newRewriteTestEngine builds a minimal engine with only resolveCustomDomain
// wired, terminating in a handler that reports the (possibly rewritten) path
// it saw — enough to test the middleware in isolation from the rest of the
// real request pipeline.
func newRewriteTestEngine(t *testing.T, resolver *auth.DomainResolver, surfaces []manifest.DomainSurface, routes []manifest.Route) (*gin.Engine, *dispatcher.Dispatcher) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	reg := registry.New()
	disp := dispatcher.New(reg, protection.NewCircuitBreaker(5, 0), protection.NewBulkhead(10), config.Defaults())
	disp.AddRoutes("id1", "schoolyze", "internal", routes)
	if err := reg.Register("id1", "schoolyze", "http://plugin:1", "1.0.0", "internal",
		manifest.Manifest{DomainSurfaces: surfaces},
		func(u *url.URL) *httputil.ReverseProxy { return &httputil.ReverseProxy{} }); err != nil {
		t.Fatalf("register: %v", err)
	}

	engine := gin.New()
	engine.Use(resolveCustomDomain(disp, resolver))
	engine.NoRoute(func(c *gin.Context) {
		c.String(http.StatusOK, c.Request.URL.Path)
	})
	return engine, disp
}

func fakeIdentity(t *testing.T, hostname string, res auth.ResolvedDomain) *auth.DomainResolver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res)
	}))
	t.Cleanup(srv.Close)
	return auth.NewDomainResolver(fakeBaseURLLookup{srv.URL}, "secret")
}

type fakeBaseURLLookup struct{ url string }

func (f fakeBaseURLLookup) GetBaseURL(string) (string, bool) { return f.url, true }

// The whole point: a request on a claimed custom domain, with no path prefix
// at all, is rewritten to the canonical slug-prefixed path an ordinary
// plugin route already handles.
func TestResolveCustomDomain_RewritesPath(t *testing.T) {
	resolver := fakeIdentity(t, "portal.acme.com", auth.ResolvedDomain{
		TenantSlug: "acme", Surface: "portal",
	})
	engine, _ := newRewriteTestEngine(t, resolver,
		[]manifest.DomainSurface{{Surface: "portal", PathPrefix: "/school/portal"}},
		[]manifest.Route{{Method: "GET", Path: "/school/*"}},
	)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Host = "portal.acme.com"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Body.String() != "/school/portal/acme/login" {
		t.Errorf("rewritten path = %q, want /school/portal/acme/login", w.Body.String())
	}
}

// A bare root path ("/") rewrites to just prefix+slug, no trailing segment —
// what the website surface's /site/:slug route expects.
func TestResolveCustomDomain_RewritesRootPath(t *testing.T) {
	resolver := fakeIdentity(t, "acme.com", auth.ResolvedDomain{
		TenantSlug: "acme", Surface: "website",
	})
	engine, _ := newRewriteTestEngine(t, resolver,
		[]manifest.DomainSurface{{Surface: "website", PathPrefix: "/site"}},
		[]manifest.Route{{Method: "GET", Path: "/site/*"}},
	)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "acme.com"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Body.String() != "/site/acme" {
		t.Errorf("rewritten path = %q, want /site/acme", w.Body.String())
	}
}

// A path that already routes normally (e.g. the platform's own domain) is
// left untouched — the middleware never even asks Identity.
func TestResolveCustomDomain_NoOpWhenAlreadyRoutable(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		json.NewEncoder(w).Encode(auth.ResolvedDomain{})
	}))
	defer srv.Close()
	resolver := auth.NewDomainResolver(fakeBaseURLLookup{srv.URL}, "secret")

	engine, _ := newRewriteTestEngine(t, resolver,
		[]manifest.DomainSurface{{Surface: "portal", PathPrefix: "/school/portal"}},
		[]manifest.Route{{Method: "GET", Path: "/school/*"}},
	)

	req := httptest.NewRequest(http.MethodGet, "/school/portal/acme/login", nil)
	req.Host = "gateway.example.com"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Body.String() != "/school/portal/acme/login" {
		t.Errorf("path changed to %q, want it left alone", w.Body.String())
	}
	if called {
		t.Error("Identity was called for a path that already routes normally")
	}
}

// A nil resolver (no PLUGIN_API_KEY configured) disables the feature
// entirely — the request passes through unchanged, same as before this
// middleware existed.
func TestResolveCustomDomain_NilResolverIsNoop(t *testing.T) {
	engine, _ := newRewriteTestEngine(t, nil,
		[]manifest.DomainSurface{{Surface: "portal", PathPrefix: "/school/portal"}},
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Host = "portal.acme.com"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Body.String() != "/login" {
		t.Errorf("path = %q, want unchanged /login", w.Body.String())
	}
}

// An unclaimed Host (Identity returns 404) leaves the path alone — the
// request falls through to the ordinary ("no plugin handles this route") 404.
func TestResolveCustomDomain_UnclaimedHostIsNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	resolver := auth.NewDomainResolver(fakeBaseURLLookup{srv.URL}, "secret")

	engine, _ := newRewriteTestEngine(t, resolver,
		[]manifest.DomainSurface{{Surface: "portal", PathPrefix: "/school/portal"}},
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	req.Host = "randomsite.com"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if w.Body.String() != "/login" {
		t.Errorf("path = %q, want unchanged /login", w.Body.String())
	}
}

// A session-scoped surface gets the prefix and no slug — its tenant is the
// session's, and writing one into the URL would invent a second, weaker answer
// to a question already settled. The prefix header goes with it so the plugin
// renders links without it.
func TestResolveCustomDomain_SessionScopedWritesNoSlug(t *testing.T) {
	resolver := fakeIdentity(t, "panel.acme.edu.bd", auth.ResolvedDomain{
		TenantID: "t_acme", TenantSlug: "acme", Surface: "panel",
	})
	engine, _ := newRewriteTestEngine(t, resolver,
		[]manifest.DomainSurface{{Surface: "panel", PathPrefix: "/school", SessionScoped: true}},
		[]manifest.Route{{Method: "GET", Path: "/school/*"}},
	)

	var gotPrefix string
	engine.NoRoute(func(c *gin.Context) {
		gotPrefix = c.Request.Header.Get(middleware.HeaderHostPrefix)
		c.String(http.StatusOK, c.Request.URL.Path)
	})

	req := httptest.NewRequest(http.MethodGet, "/students", nil)
	req.Host = "panel.acme.edu.bd"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if got := w.Body.String(); got != "/school/students" {
		t.Errorf("rewritten path = %q, want /school/students — a slug here would be a tenant claim from the URL", got)
	}
	if gotPrefix != "/school" {
		t.Errorf("%s = %q, want /school", middleware.HeaderHostPrefix, gotPrefix)
	}
}

// The check the slug used to perform, moved to the only party that can make it.
// A session for one institution reaching another's hostname is refused; nothing
// downstream sees the request.
func TestRequireHostTenant(t *testing.T) {
	cases := []struct {
		name     string
		resolved string // tenant the hostname resolved to; "" = not a custom domain
		session  string // tenant the caller's session belongs to; "" = no session
		wantCode int
	}{
		{"host and session agree", "t_acme", "t_acme", http.StatusOK},
		{"a session for another institution", "t_acme", "t_beta", http.StatusForbidden},
		// Not signed in: the login page is where this ends, not another
		// institution's records.
		{"no session at all", "t_acme", "", http.StatusOK},
		// Every ordinary request, on the platform's own hostname.
		{"not a session-scoped domain", "", "t_beta", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				if tc.resolved != "" {
					c.Set(resolvedTenantKey, tc.resolved)
				}
				if tc.session != "" {
					middleware.SetIdentity(c, &auth.Identity{TenantID: tc.session})
				}
				c.Next()
			})
			engine.Use(requireHostTenant())
			engine.GET("/students", func(c *gin.Context) { c.Status(http.StatusOK) })

			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/students", nil))
			if w.Code != tc.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tc.wantCode)
			}
		})
	}
}
