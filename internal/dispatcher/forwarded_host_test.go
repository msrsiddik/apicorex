package dispatcher

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/middleware"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
)

// A proxied request must carry the hostname the browser asked for.
//
// End-to-end through Dispatch and a real reverse proxy rather than a unit test
// on InjectForwardedHost, because the bug this prevents lives in the gap
// between them: the director overwrites Host on its way out (ProxyFor), so a
// plugin reading Request.Host reads the plugin's own address. Asserting both
// halves in one test is what makes that gap visible — a header assertion alone
// would still pass if the director stopped overwriting, and stop meaning
// anything.
func TestDispatch_ForwardsBrowserHost(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotForwarded, gotHost string
	plugin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotForwarded = r.Header.Get(middleware.HeaderForwardedHost)
		gotHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer plugin.Close()

	pluginURL, err := url.Parse(plugin.URL)
	if err != nil {
		t.Fatalf("parse plugin url: %v", err)
	}

	reg := registry.New()
	disp := New(reg, protection.NewCircuitBreaker(5, 0), protection.NewBulkhead(10), config.Defaults())
	routes := []manifest.Route{{Method: "GET", Path: "/school/*", Public: true}}
	disp.AddRoutes("id1", "schoolyze", "internal", routes)
	if err := reg.Register("id1", "schoolyze", plugin.URL, "1.0.0", "internal",
		manifest.Manifest{Routes: routes}, disp.ProxyFor); err != nil {
		t.Fatalf("register: %v", err)
	}

	engine := gin.New()
	engine.NoRoute(disp.Dispatch)
	// A real server rather than ServeHTTP into a recorder: ReverseProxy asserts
	// its ResponseWriter is an http.CloseNotifier, which a recorder is not.
	gateway := httptest.NewServer(engine)
	defer gateway.Close()

	req, err := http.NewRequest(http.MethodGet, gateway.URL+"/school/portal", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// What the browser asked for, which is the whole subject of this test.
	req.Host = "abc.example.com"

	resp, err := gateway.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotForwarded != "abc.example.com" {
		t.Errorf("%s = %q, want %q — a plugin has no other way to learn the hostname a guardian typed",
			middleware.HeaderForwardedHost, gotForwarded, "abc.example.com")
	}
	if gotHost != pluginURL.Host {
		t.Errorf("Host = %q, want %q — the director must still address the plugin", gotHost, pluginURL.Host)
	}
}

// Public routes are the case that needs this most: the Portal resolves an
// institution from its hostname and Core introspects nothing there, so an
// injection hung off the resolved identity would skip exactly the surface it
// was added for.
func TestInjectForwardedHost_NoIdentityRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/school/portal", nil)
	c.Request.Host = "abc.example.com"

	middleware.InjectForwardedHost(c)

	if got := c.Request.Header.Get(middleware.HeaderForwardedHost); got != "abc.example.com" {
		t.Errorf("%s = %q, want %q with no identity resolved",
			middleware.HeaderForwardedHost, got, "abc.example.com")
	}
}
