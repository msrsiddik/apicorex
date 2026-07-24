// Package server wires Core's single HTTP server: the control-plane endpoints
// (/_core/*), the docs (Scalar UI + merged OpenAPI), the embedded gateway
// dashboard (/dashboard), /health, /plugins, /metrics, the auth + tenant-header
// middleware chain, and the catch-all dispatcher that proxies everything else
// to plugins.
package server

import (
	"context"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/auth"
	"github.com/msrsiddik/apicorex/internal/controlplane"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/middleware"
	"github.com/msrsiddik/apicorex/internal/openapi"
	"github.com/msrsiddik/apicorex/internal/registry"
	ginopenapi "github.com/oaswrap/spec/adapter/ginopenapi"
	"github.com/oaswrap/spec/option"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// HTTPServer is Core's HTTP server (data plane + control plane + docs).
type HTTPServer struct {
	engine  *gin.Engine
	addr    string
	httpSrv *http.Server
}

// NewHTTP builds the configured server: it mounts the control plane, core
// endpoints, docs, and the middleware chain, and sets the dispatcher as the
// catch-all handler. introspector may be nil to disable auth (dev only).
func NewHTTP(
	reg *registry.Registry,
	disp *dispatcher.Dispatcher,
	injector *openapi.Injector,
	introspector *auth.Introspector,
	cpHandlers *controlplane.Handlers,
	dashboardHandler gin.HandlerFunc,
	addr string,
) *HTTPServer {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())

	// CORS first — browser preflight (OPTIONS) must get CORS headers, not 401.
	// Allowed origins come from CORS_ALLOWED_ORIGINS (comma-separated); empty = any.
	engine.Use(middleware.CORS(middleware.ParseOrigins(os.Getenv("CORS_ALLOWED_ORIGINS"))))

	// control plane: plugins register/heartbeat/deregister here
	cpHandlers.Mount(engine)

	// disable built-in docs/spec endpoints — we serve our own merged spec + Scalar UI
	r := ginopenapi.NewRouter(engine,
		option.WithTitle("ApiCoreX"),
		option.WithVersion("1.0.0"),
		option.WithDisableDocs(),
		option.WithSecurity("bearerAuth", option.SecurityHTTPBearer("Bearer")),
		// Require it globally so Scalar's "Authorize" button applies to every
		// operation (Core's own + every merged plugin spec), not just ones that
		// happen to declare per-operation security.
		option.WithGlobalSecurity("bearerAuth"),
	)

	// Prometheus metrics — no auth
	engine.GET("/metrics", gin.WrapH(promhttp.Handler()))

	// health — no auth
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}).With(option.Summary("Health check"), option.Tags("core"))

	// registered plugins list — no auth
	r.GET("/plugins", func(c *gin.Context) {
		entries := reg.List()
		out := make([]gin.H, 0, len(entries))
		for _, e := range entries {
			ps := disp.ProtectionStatus(e.Info.PluginID)
			out = append(out, gin.H{
				"plugin_id":       e.Info.PluginID,
				"plugin_name":     e.Info.PluginName,
				"version":         e.Info.Version,
				"base_url":        e.Info.BaseURL,
				"status":          e.Info.Status,
				"alive":           e.Alive,
				"registered_at":   e.RegisteredAt,
				"last_heartbeat":  e.LastHeartbeat,
				"routes":          e.Manifest.Routes,
				"circuit_state":   ps.CircuitState,
				"bulkhead_active": ps.BulkheadActive,
				"bulkhead_max":    ps.BulkheadMax,
				"rate_tokens":     ps.RateTokens,
				"rate_burst":      ps.RateBurst,
			})
		}
		c.JSON(http.StatusOK, out)
	}).With(option.Summary("List registered plugins"), option.Tags("core"))

	// merged OpenAPI spec — Core base spec + all registered plugin specs.
	// Gated on the dashboard session cookie (see RequireDashboardSession): the
	// spec exposes every plugin's routes and schemas, so it shouldn't be public.
	engine.GET("/docs/openapi.json", cpHandlers.RequireDashboardSession, func(c *gin.Context) {
		base, err := r.MarshalJSON()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "spec generation failed"})
			return
		}
		merged := injector.MergedSpec(base)
		c.Data(http.StatusOK, "application/json", merged)
	})

	// Scalar UI — reads from /docs/openapi.json which includes all plugin routes.
	// Same session-cookie gate; log in via /dashboard first.
	engine.GET("/docs", cpHandlers.RequireDashboardSession, func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", scalarHTML("ApiCoreX", "/docs/openapi.json"))
	})

	// gateway dashboard — embedded Next.js SPA. The static assets are public;
	// the SPA itself gates on DASHBOARD_SECRET via its own login screen, and
	// its write actions are session-token gated (see controlplane admin routes).
	if dashboardHandler != nil {
		engine.GET("/dashboard/*filepath", dashboardHandler)
	}

	// strip any client-supplied X-ApiCoreX-* headers (anti-spoofing) on every request
	engine.Use(middleware.StripSpoofedHeaders())

	// auth middleware (device-token bearer, for proxied plugin routes) — skip
	// core endpoints, control plane, and plugin public routes. /docs is skipped
	// here too since it isn't part of the proxied API; it has its own
	// dashboard-session-cookie gate registered above (RequireDashboardSession).
	authMiddleware := middleware.Auth(introspector)
	engine.Use(func(c *gin.Context) {
		p := c.Request.URL.Path
		if p == "/health" || p == "/plugins" || p == "/metrics" || strings.HasPrefix(p, "/docs") || strings.HasPrefix(p, "/_core") || strings.HasPrefix(p, "/dashboard") {
			c.Next()
			return
		}
		if disp.IsPublic(c.Request.Method, p) {
			c.Next()
			return
		}
		authMiddleware(c)
	})

	engine.NoRoute(disp.Dispatch)

	log.Printf("[http] configured with Scalar UI at /docs")

	return &HTTPServer{
		engine: engine,
		addr:   addr,
		httpSrv: &http.Server{
			Addr:    addr,
			Handler: engine,
		},
	}
}

// scalarHTML returns a minimal Scalar UI page that loads the given specURL.
func scalarHTML(title, specURL string) []byte {
	const tpl = `<!doctype html>
<html>
<head>
	<title>{{.Title}} - Scalar</title>
	<meta charset="utf-8"/>
	<meta name="viewport" content="width=device-width, initial-scale=1"/>
</head>
<body>
<script id="api-reference" data-url="{{.SpecURL}}"></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body>
</html>`

	t := template.Must(template.New("scalar").Parse(tpl))
	var buf strings.Builder
	if err := t.Execute(&buf, struct{ Title, SpecURL string }{title, specURL}); err != nil {
		return []byte(fmt.Sprintf("template error: %v", err))
	}
	return []byte(buf.String())
}

// Start runs the server until it is shut down (blocking).
func (s *HTTPServer) Start(_ context.Context) error {
	log.Printf("[http] listening on %s", s.addr)
	if err := s.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
