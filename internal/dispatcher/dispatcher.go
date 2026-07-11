// Package dispatcher is the gateway data plane. It matches each incoming HTTP
// request to the plugin that owns the route, applies the protection layers
// (firewall, rate limit, bulkhead, circuit breaker), injects verified tenant
// context as headers, and streams the request to the plugin via an
// httputil.ReverseProxy. WebSocket upgrades are proxied by connection hijacking.
package dispatcher

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/msrsiddik/apicorex/internal/auth"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/middleware"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
	"github.com/msrsiddik/apicorex/internal/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type routeEntry struct {
	pluginID   string
	pluginName string
	method     string // "*" = any
	pattern    string // e.g. "/auth/login", "/billing/*"
	public     bool
	permission string // required permission; "" = any authenticated
}

// Dispatcher routes HTTP requests to registered plugins and proxies them with
// streaming support. It holds an in-memory route table plus a per-plugin rate
// limiter, and shares a circuit breaker and bulkhead across all plugins. A
// Dispatcher is safe for concurrent use.
type Dispatcher struct {
	reg *registry.Registry
	cb  *protection.CircuitBreaker
	bh  *protection.Bulkhead
	cfg config.Config

	transport *http.Transport

	mu       sync.RWMutex
	routes   []routeEntry
	pluginRL map[string]*protection.RateLimiter
}

// New builds a Dispatcher. reg supplies plugin targets, cb and bh are the shared
// circuit breaker and bulkhead, and cfg provides the rate/limit settings applied
// per plugin in AddRoutes.
func New(reg *registry.Registry, cb *protection.CircuitBreaker, bh *protection.Bulkhead, cfg config.Config) *Dispatcher {
	return &Dispatcher{
		reg:      reg,
		cb:       cb,
		bh:       bh,
		cfg:      cfg,
		pluginRL: make(map[string]*protection.RateLimiter),
		transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
	}
}

// ProxyFor builds a streaming reverse proxy targeting the plugin base URL.
// Passed to the registry so each plugin entry carries its own proxy.
func (d *Dispatcher) ProxyFor(target *url.URL) *httputil.ReverseProxy {
	proxy := &httputil.ReverseProxy{
		Transport:     d.transport,
		FlushInterval: -1, // flush immediately — SSE / streaming
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.Host = target.Host
			// path is left as-is — plugins serve the same paths Core routes
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[proxy] error proxying %s %s: %v", r.Method, r.URL.Path, err)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"plugin unavailable"}`))
		},
	}
	return proxy
}

// AddRoutes registers a plugin's routes in the dispatch table and creates its
// per-plugin rate limiter from config. Calling it again for the same pluginID
// replaces the plugin's existing routes. pluginType ("internal" or "public")
// selects the default rate when the plugin has no explicit config override.
func (d *Dispatcher) AddRoutes(pluginID, pluginName, pluginType string, routes []manifest.Route) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removeRoutes(pluginID)
	for _, r := range routes {
		d.routes = append(d.routes, routeEntry{
			pluginID:   pluginID,
			pluginName: pluginName,
			method:     strings.ToUpper(r.Method),
			pattern:    r.Path,
			public:     r.Public,
			permission: r.Permission,
		})
	}
	limits := d.cfg.For(pluginName)
	rate := limits.RatePerSec
	burst := limits.RateBurst
	// "public" plugins default to 1/10th rate unless explicitly overridden in config
	if pluginType == "public" {
		if _, overridden := d.cfg.Plugins[pluginName]; !overridden {
			rate = rate / 10
			burst = burst / 10
		}
	}
	d.pluginRL[pluginID] = protection.NewRateLimiter(rate, burst)
}

// RemoveRoutes drops all routes and the rate limiter for a plugin (called on
// deregister).
func (d *Dispatcher) RemoveRoutes(pluginID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.removeRoutes(pluginID)
	delete(d.pluginRL, pluginID)
}

func (d *Dispatcher) removeRoutes(pluginID string) {
	filtered := d.routes[:0]
	for _, r := range d.routes {
		if r.pluginID != pluginID {
			filtered = append(filtered, r)
		}
	}
	d.routes = filtered
}

func (d *Dispatcher) match(method, path string) *routeEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()
	for i := range d.routes {
		r := &d.routes[i]
		if methodMatch(r.method, method) && r.pattern == path {
			return r
		}
	}
	for i := range d.routes {
		r := &d.routes[i]
		if !methodMatch(r.method, method) {
			continue
		}
		// gin-style :param segments match any single segment
		if patternMatch(r.pattern, path) {
			return r
		}
		prefix := strings.TrimSuffix(r.pattern, "/*")
		if prefix != r.pattern && strings.HasPrefix(path, prefix) {
			return r
		}
	}
	return nil
}

func methodMatch(pattern, method string) bool {
	return pattern == "*" || pattern == method
}

// patternMatch matches a gin-style pattern (with :param) against a concrete path.
func patternMatch(pattern, path string) bool {
	pp := strings.Split(strings.Trim(pattern, "/"), "/")
	cp := strings.Split(strings.Trim(path, "/"), "/")
	if len(pp) != len(cp) {
		return false
	}
	for i := range pp {
		if strings.HasPrefix(pp[i], ":") {
			continue // param matches any segment
		}
		if pp[i] != cp[i] {
			return false
		}
	}
	return true
}

// IsPublic returns true if the matched route is public (no token required).
func (d *Dispatcher) IsPublic(method, path string) bool {
	entry := d.match(method, path)
	return entry != nil && entry.public
}

// authorized reports whether the resolved identity (the ACTING user, fresh
// from introspection) satisfies entry's declared permission: either the
// permission itself (wildcards honored) or platform_admin, since platform
// admins act across tenants by design and may hold no tenant role — and thus no
// tenant-scoped permissions — at all. Callers only invoke this when
// entry.permission != "" (an empty permission means "any authenticated user").
func authorized(id *auth.Identity, entry *routeEntry) bool {
	if id == nil {
		return false
	}
	if id.UserType == "platform_admin" {
		return true
	}
	return middleware.PermissionAllowed(id.Permissions, entry.permission)
}

// Dispatch is the Gin catch-all handler. It matches the request to a plugin,
// runs the protection layers, injects tenant headers, starts a trace span, and
// proxies the request (streaming, or hijacked for WebSocket). It records metrics
// and a structured log line for every request.
func (d *Dispatcher) Dispatch(c *gin.Context) {
	start := time.Now()
	path := c.Request.URL.Path
	method := c.Request.Method
	requestID := c.GetHeader("X-Request-ID")
	if requestID == "" {
		requestID = uuid.New().String()
		c.Request.Header.Set("X-Request-ID", requestID)
		c.Header("X-Request-ID", requestID)
	}

	// Layer 1: firewall
	if protection.IsRouteBlocked(path) {
		protection.RequestsRejected.WithLabelValues("_firewall", "blocked").Inc()
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	entry := d.match(method, path)
	if entry == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "no plugin handles this route"})
		return
	}
	plugin := entry.pluginName

	// Authorization: a non-public route declaring a permission requires the
	// caller's verified claims to satisfy it. Auth middleware has already run,
	// so claims are present for protected routes.
	if !entry.public && entry.permission != "" && !authorized(middleware.IdentityFrom(c), entry) {
		protection.RequestsRejected.WithLabelValues(plugin, "forbidden").Inc()
		c.JSON(http.StatusForbidden, gin.H{"error": "missing permission: " + entry.permission})
		return
	}

	pluginEntry, ok := d.reg.Get(entry.pluginID)
	if !ok || !pluginEntry.Alive {
		protection.RequestsRejected.WithLabelValues(plugin, "unavailable").Inc()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "plugin unavailable"})
		return
	}

	// Layer 2: rate limiter
	d.mu.RLock()
	rl := d.pluginRL[entry.pluginID]
	d.mu.RUnlock()
	if rl != nil && !rl.Allow(entry.pluginID) {
		protection.RequestsRejected.WithLabelValues(plugin, "rate_limit").Inc()
		c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
		return
	}

	// Layer 3: bulkhead
	if err := d.bh.Acquire(entry.pluginID); err != nil {
		protection.RequestsRejected.WithLabelValues(plugin, "bulkhead").Inc()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "plugin busy"})
		return
	}
	defer d.bh.Release(entry.pluginID)

	// Layer 4: circuit breaker
	if err := d.cb.Allow(entry.pluginID); err != nil {
		protection.RequestsRejected.WithLabelValues(plugin, "circuit_breaker").Inc()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "service temporarily unavailable"})
		return
	}

	// inject trusted tenant headers (claims set by auth middleware; nil for public routes)
	middleware.InjectTenantHeaders(c)

	tenantID := c.Request.Header.Get(middleware.HeaderTenantID)

	// start a span for the proxied request; propagate trace context to the plugin
	ctx, span := tracing.Tracer().Start(c.Request.Context(), "proxy "+plugin,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			tracing.PluginAttr(plugin),
			attribute.String("http.method", method),
			attribute.String("http.route", entry.pattern),
			attribute.String("tenant.id", tenantID),
		),
	)
	defer span.End()
	req := c.Request.WithContext(ctx)
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	protection.RequestsInFlight.WithLabelValues(plugin).Inc()
	defer protection.RequestsInFlight.WithLabelValues(plugin).Dec()

	// WebSocket upgrade → hijack proxy
	if isWebSocketUpgrade(req) {
		d.proxyWebSocket(c, pluginEntry)
		d.cb.RecordSuccess(entry.pluginID)
		protection.RequestsTotal.WithLabelValues(plugin, "WS", "websocket").Inc()
		return
	}

	// HTTP → streaming reverse proxy
	rec := &statusRecorder{ResponseWriter: c.Writer, status: http.StatusOK}
	pluginEntry.Proxy.ServeHTTP(rec, req)
	span.SetAttributes(attribute.Int("http.status_code", rec.status))

	duration := time.Since(start)
	if rec.status >= 500 {
		d.cb.RecordFailure(entry.pluginID)
	} else {
		d.cb.RecordSuccess(entry.pluginID)
	}
	protection.CircuitBreakerState.WithLabelValues(plugin).Set(d.cb.StateMetric(entry.pluginID))

	protection.RequestsTotal.WithLabelValues(plugin, method, protection.StatusClass(rec.status)).Inc()
	protection.RequestDuration.WithLabelValues(plugin, method).Observe(duration.Seconds())
	protection.LogRequest(protection.RequestLog{
		PluginID: entry.pluginID, TenantID: tenantID,
		Path: path, Method: method, Status: rec.status,
		Duration: duration, RequestID: requestID,
	})
}

// statusRecorder captures the response status for logging / circuit breaker.
type statusRecorder struct {
	gin.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.written {
		r.status = code
		r.written = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// proxyWebSocket hijacks the client connection and pipes it to the plugin,
// preserving full-duplex WebSocket traffic.
func (d *Dispatcher) proxyWebSocket(c *gin.Context, entry *registry.PluginEntry) {
	target := entry.Target

	// dial the plugin
	pluginConn, err := net.DialTimeout("tcp", target.Host, 10*time.Second)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "cannot reach plugin"})
		return
	}
	defer pluginConn.Close()

	// hijack the client connection
	hj, ok := c.Writer.(http.Hijacker)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "hijack unsupported"})
		return
	}
	clientConn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	defer clientConn.Close()

	// replay the (modified) handshake request to the plugin
	if err := c.Request.Write(pluginConn); err != nil {
		return
	}

	// bidirectional copy
	errc := make(chan error, 2)
	go func() { _, e := io.Copy(pluginConn, clientConn); errc <- e }()
	go func() { _, e := io.Copy(clientConn, pluginConn); errc <- e }()
	<-errc
}
