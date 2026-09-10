package server

import (
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/middleware"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
)

// newProductHostEngine wires only productHostRewrite, terminating in a handler
// that reports the path it saw, the prefix header, and whether the path it was
// handed still classifies as public — the last one because the rewrite runs
// before the auth split and a path that stops being recognised there is a login
// wall on a page that never had one.
func newProductHostEngine(t *testing.T, hosts map[string]string, routes []manifest.Route) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	reg := registry.New()
	disp := dispatcher.New(reg, protection.NewCircuitBreaker(5, 0), protection.NewBulkhead(10), config.Defaults())
	disp.AddRoutes("id1", "schoolyze", "internal", routes)
	if err := reg.Register("id1", "schoolyze", "http://plugin:1", "1.0.0", "internal",
		manifest.Manifest{Routes: routes},
		func(u *url.URL) *httputil.ReverseProxy { return &httputil.ReverseProxy{} }); err != nil {
		t.Fatalf("register: %v", err)
	}

	engine := gin.New()
	engine.Use(productHostRewrite(disp, hosts))
	engine.NoRoute(func(c *gin.Context) {
		c.String(http.StatusOK, "%s|%s|%v",
			c.Request.URL.Path,
			c.Request.Header.Get(middleware.HeaderHostPrefix),
			disp.IsPublic(c.Request.Method, c.Request.URL.Path))
	})
	return engine
}

func TestProductHostRewrite(t *testing.T) {
	hosts := map[string]string{"panel.example.com": "/school"}
	routes := []manifest.Route{
		{Method: "GET", Path: "/school/*", Public: true},
		{Method: "GET", Path: "/accounting/*", Public: true},
	}

	cases := []struct {
		name       string
		host       string
		path       string
		wantPath   string
		wantPrefix string
	}{
		{
			name:     "a path the product host owns gets its prefix back",
			host:     "panel.example.com",
			path:     "/students",
			wantPath: "/school/students", wantPrefix: "/school",
		},
		{
			// The whole reason for the IsRoutable check. Identity serves /login
			// at the gateway root; rewriting it would delete sign-in.
			name:     "a path something else already serves is untouched",
			host:     "panel.example.com",
			path:     "/accounting/ledger",
			wantPath: "/accounting/ledger", wantPrefix: "",
		},
		{
			name:     "the bare root becomes the prefix itself, with no trailing slash",
			host:     "panel.example.com",
			path:     "/",
			wantPath: "/school", wantPrefix: "/school",
		},
		{
			name:     "a port on the Host does not stop the lookup",
			host:     "panel.example.com:8443",
			path:     "/students",
			wantPath: "/school/students", wantPrefix: "/school",
		},
		{
			name:     "matching is case-insensitive, as hostnames are",
			host:     "Panel.Example.COM",
			path:     "/students",
			wantPath: "/school/students", wantPrefix: "/school",
		},
		{
			name:     "an unconfigured host is left to 404 exactly as before",
			host:     "gateway.example.com",
			path:     "/students",
			wantPath: "/students", wantPrefix: "",
		},
		{
			// Rewriting would carry it out of the namespace it named.
			name:     "a firewall-blocked path stays blocked",
			host:     "panel.example.com",
			path:     "/admin/users",
			wantPath: "/admin/users", wantPrefix: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := newProductHostEngine(t, hosts, routes)
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req.Host = tc.host
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)

			gotPath, gotPrefix, _ := cut3(w.Body.String())
			if gotPath != tc.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if gotPrefix != tc.wantPrefix {
				t.Errorf("%s = %q, want %q", middleware.HeaderHostPrefix, gotPrefix, tc.wantPrefix)
			}
		})
	}
}

// The bare root is the one rewrite that produces no header, because "/school"
// is where the prefix and the path coincide — and the plugin still has to know
// to render prefix-less links there, or the first page a visitor lands on is
// the one page whose links all point at the other host's URL shape.
func TestProductHostRewrite_RootStillNamesThePrefix(t *testing.T) {
	engine := newProductHostEngine(t,
		map[string]string{"panel.example.com": "/school"},
		[]manifest.Route{{Method: "GET", Path: "/school/*", Public: true}})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "panel.example.com"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	_, gotPrefix, _ := cut3(w.Body.String())
	if gotPrefix != "/school" {
		t.Errorf("%s = %q, want %q on the landing page too",
			middleware.HeaderHostPrefix, gotPrefix, "/school")
	}
}

// The rewrite runs before the public/auth split, so the path it produces is the
// one IsPublic is asked about. If it did not line up, every page on the product
// host would demand a bearer token the browser has no way to supply.
func TestProductHostRewrite_RewrittenPathIsStillPublic(t *testing.T) {
	engine := newProductHostEngine(t,
		map[string]string{"panel.example.com": "/school"},
		[]manifest.Route{{Method: "GET", Path: "/school/*", Public: true}})

	req := httptest.NewRequest(http.MethodGet, "/students", nil)
	req.Host = "panel.example.com"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if _, _, public := cut3(w.Body.String()); public != "true" {
		t.Errorf("IsPublic after rewrite = %s, want true", public)
	}
}

// Nothing configured must behave exactly as it did before this existed.
func TestProductHostRewrite_NoConfigIsANoOp(t *testing.T) {
	engine := newProductHostEngine(t, nil,
		[]manifest.Route{{Method: "GET", Path: "/school/*", Public: true}})

	req := httptest.NewRequest(http.MethodGet, "/students", nil)
	req.Host = "panel.example.com"
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)

	if gotPath, gotPrefix, _ := cut3(w.Body.String()); gotPath != "/students" || gotPrefix != "" {
		t.Errorf("got (%q, %q), want (\"/students\", \"\")", gotPath, gotPrefix)
	}
}

func cut3(s string) (a, b, c string) {
	parts := strings.SplitN(s, "|", 3)
	for len(parts) < 3 {
		parts = append(parts, "")
	}
	return parts[0], parts[1], parts[2]
}

func TestParseProductHosts(t *testing.T) {
	ok := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"empty is not configured", "", nil},
		{"one entry", "panel.example.com=/school", map[string]string{"panel.example.com": "/school"}},
		{"several, with whitespace", " panel.example.com=/school , gl.example.com=/accounting ",
			map[string]string{"panel.example.com": "/school", "gl.example.com": "/accounting"}},
		{"a prefix is normalised", "panel.example.com=school/", map[string]string{"panel.example.com": "/school"}},
		{"a host is lowercased and de-ported", "Panel.Example.COM:8443=/school",
			map[string]string{"panel.example.com": "/school"}},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseProductHosts(tc.raw)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for h, p := range tc.want {
				if got[h] != p {
					t.Errorf("%s = %q, want %q", h, got[h], p)
				}
			}
		})
	}

	// Every one of these is a hostname that would otherwise serve 404s for a
	// reason no log line explains.
	bad := []struct {
		name string
		raw  string
	}{
		{"no prefix at all", "panel.example.com"},
		{"empty host", "=/school"},
		{"a URL rather than a hostname", "https://panel.example.com=/school"},
		{"a path rather than a hostname", "panel.example.com/panel=/school"},
		{"an empty prefix", "panel.example.com="},
		{"the root as a prefix", "panel.example.com=/"},
		{"a reserved prefix", "panel.example.com=/admin"},
		{"one host mapped twice", "panel.example.com=/school,panel.example.com=/accounting"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseProductHosts(tc.raw); err == nil {
				t.Errorf("parseProductHosts(%q) = nil error, want a refusal to start", tc.raw)
			}
		})
	}
}
