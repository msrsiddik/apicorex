// Package controlplane implements the HTTP control plane: plugins register,
// heartbeat, and deregister here. Core pulls each plugin's manifest from
// GET {base_url}/_apicorex/manifest.
package controlplane

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/openapi"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
)

type Handlers struct {
	reg           *registry.Registry
	disp          *dispatcher.Dispatcher
	injector      *openapi.Injector
	apiKey        string
	allowlist     map[string]bool // empty = allow any (dev)
	signer        *tokenSigner
	dashSecret    string
	sessionSigner *tokenSigner
	client        *http.Client
}

// New builds the control-plane handlers. allowlist is the set of plugin names
// permitted to register (empty slice = allow any, for dev). The signer secret
// should be a strong secret (reuse JWT_SECRET or a dedicated one). dashSecret
// gates the gateway dashboard's login (a single shared key, not a
// username/password pair) — empty means login is disabled (dev only),
// matching the rest of Core's dev-mode posture.
func New(reg *registry.Registry, disp *dispatcher.Dispatcher, injector *openapi.Injector, apiKey string, allowlist []string, signerSecret, dashSecret string) *Handlers {
	al := make(map[string]bool, len(allowlist))
	for _, n := range allowlist {
		if n = strings.TrimSpace(n); n != "" {
			al[n] = true
		}
	}
	return &Handlers{
		reg:        reg,
		disp:       disp,
		injector:   injector,
		apiKey:     apiKey,
		allowlist:  al,
		signer:     newTokenSigner(signerSecret, 24*time.Hour),
		dashSecret: dashSecret,
		// Derived from dashSecret, not signerSecret: the two are independent
		// env vars (PLUGIN_API_KEY vs DASHBOARD_SECRET), and PLUGIN_API_KEY may
		// be unset in dev. If it were reused here, an unset PLUGIN_API_KEY would
		// make the HMAC key a fixed, source-visible string (":dashboard"),
		// letting anyone forge session tokens even with DASHBOARD_SECRET set.
		// When dashSecret is itself empty, requireSession no-ops regardless of
		// signature validity, so a weak/guessable key here is harmless.
		sessionSigner: newTokenSigner(dashSecret+":session", 12*time.Hour),
		client:        &http.Client{Timeout: 10 * time.Second},
	}
}

// Mount registers the /_core/* routes on the engine.
func (h *Handlers) Mount(engine *gin.Engine) {
	g := engine.Group("/_core")
	g.POST("/register", h.register)
	g.POST("/heartbeat", h.heartbeat)
	g.POST("/deregister", h.deregister)
	g.GET("/plugins/:name/manifest", h.pluginManifest)

	// dashboard login — unauthenticated by definition (this is where a session
	// starts). /login-required lets the frontend know whether to show a login
	// form at all (dev instances with no DASHBOARD_SECRET set skip it).
	g.GET("/admin/login-required", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"required": h.dashSecret != ""})
	})
	g.POST("/admin/login", h.login)
	g.POST("/admin/logout", h.logout)

	// operator actions from the gateway dashboard — gated by a session token
	// from /admin/login. Login disabled (dashSecret == "") means these are
	// open, matching the rest of Core's dev-mode posture.
	admin := g.Group("/admin", h.requireSession)
	admin.GET("/session", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	admin.POST("/plugins/:id/reset-breaker", h.resetBreaker)
	admin.POST("/plugins/:id/deregister", h.adminDeregister)
}

// sessionCookieName is set on login alongside the returned Bearer token: the
// dashboard SPA uses the token (via localStorage + Authorization header) for
// its own /admin/* calls, while the cookie lets browser-navigated pages like
// /docs (and Scalar's same-origin fetch of /docs/openapi.json) authenticate
// without any custom header.
const sessionCookieName = "apicorex_session"

// login validates the dashboard secret key against DASHBOARD_SECRET and
// issues a signed session token.
func (h *Handlers) login(c *gin.Context) {
	var req struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if h.dashSecret == "" {
		// login disabled (dev) — issue a token anyway so the frontend flow works
		token := h.sessionSigner.issue("dev")
		h.setSessionCookie(c, token)
		c.JSON(http.StatusOK, gin.H{"token": token})
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Key), []byte(h.dashSecret)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid key"})
		return
	}
	log.Printf("[controlplane] dashboard: login")
	token := h.sessionSigner.issue("dashboard")
	h.setSessionCookie(c, token)
	c.JSON(http.StatusOK, gin.H{"token": token})
}

// logout clears the session cookie so a dashboard logout also revokes /docs
// access (the session token in localStorage is discarded client-side; this
// only needs to handle the cookie half of the session).
func (h *Handlers) logout(c *gin.Context) {
	secure := c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName, "", -1, "/", "", secure, true)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// setSessionCookie sets the signed session token as an HttpOnly cookie, matching
// the sessionSigner's TTL. Secure is set whenever the request itself arrived over
// TLS or behind a TLS-terminating proxy (X-Forwarded-Proto), so it also works in
// typical reverse-proxy deployments without forcing plain-HTTP dev setups to fail.
func (h *Handlers) setSessionCookie(c *gin.Context, token string) {
	secure := c.Request.TLS != nil || c.GetHeader("X-Forwarded-Proto") == "https"
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(sessionCookieName, token, int((12 * time.Hour).Seconds()), "/", "", secure, true)
}

// RequireDashboardSession gates browser-navigated dashboard-only pages (like
// /docs) on the session cookie set at login. Unlike requireSession (used by
// /admin/* API calls), it reads the cookie rather than an Authorization
// header, since these are pages the browser navigates to or fetches
// same-origin rather than an API client attaching its own headers.
func (h *Handlers) RequireDashboardSession(c *gin.Context) {
	if h.dashSecret == "" {
		c.Next()
		return
	}
	token, err := c.Cookie(sessionCookieName)
	if err != nil || token == "" {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "login required — visit /dashboard to sign in"})
		return
	}
	if _, err := h.sessionSigner.verify(token); err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired session"})
		return
	}
	c.Next()
}

// requireSession checks the Bearer session token from /admin/login.
func (h *Handlers) requireSession(c *gin.Context) {
	if h.dashSecret == "" {
		c.Next()
		return
	}
	authz := c.GetHeader("Authorization")
	token := strings.TrimPrefix(authz, "Bearer ")
	if token == "" || token == authz {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing session token"})
		return
	}
	if _, err := h.sessionSigner.verify(token); err != nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired session"})
		return
	}
	c.Next()
}

// resetBreaker manually closes a plugin's circuit breaker (operator action).
func (h *Handlers) resetBreaker(c *gin.Context) {
	pluginID := c.Param("id")
	if _, ok := h.reg.Get(pluginID); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	h.disp.ResetCircuitBreaker(pluginID)
	log.Printf("[controlplane] dashboard: circuit breaker reset for %s", pluginID)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// adminDeregister force-removes a plugin from the registry (operator action —
// unlike deregister, it needs no plugin token, since the plugin itself may be
// unreachable, which is often exactly why an operator is doing this).
func (h *Handlers) adminDeregister(c *gin.Context) {
	pluginID := c.Param("id")
	entry, ok := h.reg.Get(pluginID)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"success": false})
		return
	}
	name := entry.Info.PluginName
	h.reg.Deregister(pluginID)
	h.disp.RemoveRoutes(pluginID)
	h.injector.RemoveRoutes(name)
	protection.PluginsRegistered.Set(float64(len(h.reg.List())))
	log.Printf("[controlplane] dashboard: force-deregistered %s (%s)", name, pluginID)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

type registerReq struct {
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
}

func (h *Handlers) register(c *gin.Context) {
	var req registerReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if h.apiKey != "" && req.APIKey != h.apiKey {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid api key"})
		return
	}
	if req.BaseURL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "base_url required"})
		return
	}

	// pull the manifest from the plugin
	m, err := h.pullManifest(req.BaseURL)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("cannot pull manifest: %v", err)})
		return
	}

	// allowlist check (empty allowlist = allow any, for dev)
	if len(h.allowlist) > 0 && !h.allowlist[m.Name] {
		log.Printf("[controlplane] rejected unlisted plugin %q from %s", m.Name, req.BaseURL)
		c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("plugin %q not in allowlist", m.Name)})
		return
	}

	// Evict any stale entries for this plugin name (e.g. a previous instance that
	// restarted without deregistering) so re-registration replaces instead of
	// accumulating duplicates.
	for _, oldID := range h.reg.IDsByName(m.Name) {
		h.reg.Deregister(oldID)
		h.disp.RemoveRoutes(oldID)
	}

	pluginID := fmt.Sprintf("%s-%s", m.Name, uuid.New().String()[:8])

	if err := h.reg.Register(pluginID, m.Name, req.BaseURL, m.Version, m.PluginType, m, h.disp.ProxyFor); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.disp.AddRoutes(pluginID, m.Name, m.PluginType, m.Routes)
	h.injector.AddRoutes(m.Name, m.Routes, m.OpenAPISpec)

	// issue a signed plugin token; plugin presents it on heartbeat/deregister
	token := h.signer.issue(pluginID)

	protection.PluginsRegistered.Set(float64(len(h.reg.List())))
	log.Printf("[controlplane] registered %s (%s) at %s — %d routes", m.Name, pluginID, req.BaseURL, len(m.Routes))
	c.JSON(http.StatusOK, gin.H{"plugin_id": pluginID, "plugin_token": token})
}

func (h *Handlers) heartbeat(c *gin.Context) {
	var req struct {
		PluginID    string `json:"plugin_id"`
		PluginToken string `json:"plugin_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if !h.verifyToken(req.PluginToken, req.PluginID) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid plugin token"})
		return
	}
	if err := h.reg.Heartbeat(req.PluginID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"acknowledged": true})
}

func (h *Handlers) deregister(c *gin.Context) {
	var req struct {
		PluginID    string `json:"plugin_id"`
		PluginToken string `json:"plugin_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if !h.verifyToken(req.PluginToken, req.PluginID) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid plugin token"})
		return
	}
	entry, ok := h.reg.Get(req.PluginID)
	if !ok {
		c.JSON(http.StatusOK, gin.H{"success": false})
		return
	}
	name := entry.Info.PluginName
	h.reg.Deregister(req.PluginID)
	h.disp.RemoveRoutes(req.PluginID)
	h.injector.RemoveRoutes(name)
	protection.PluginsRegistered.Set(float64(len(h.reg.List())))
	log.Printf("[controlplane] deregistered %s", req.PluginID)
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// verifyToken checks the signed plugin token and that it matches the claimed plugin ID.
func (h *Handlers) verifyToken(token, pluginID string) bool {
	tid, err := h.signer.verify(token)
	return err == nil && tid == pluginID
}

// pluginManifest returns a registered plugin's stored manifest (Identity pulls migrations from here).
func (h *Handlers) pluginManifest(c *gin.Context) {
	name := c.Param("name")
	m, ok := h.reg.GetManifest(name)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	c.JSON(http.StatusOK, m)
}

func (h *Handlers) pullManifest(baseURL string) (manifest.Manifest, error) {
	var m manifest.Manifest
	resp, err := h.client.Get(baseURL + "/_apicorex/manifest")
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("manifest endpoint returned %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	if m.Name == "" {
		return m, fmt.Errorf("manifest missing name")
	}
	return m, nil
}
