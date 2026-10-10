// Package controlplane implements the HTTP control plane: plugins register,
// heartbeat, and deregister here. Core pulls each plugin's manifest from
// GET {base_url}/_apicorex/manifest.
package controlplane

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/openapi"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
	"github.com/msrsiddik/apicorex/internal/store"
)

type Handlers struct {
	reg            *registry.Registry
	disp           *dispatcher.Dispatcher
	injector       *openapi.Injector
	apiKey         string
	allowlist      map[string]bool // empty = allow any (dev)
	signer         *tokenSigner
	apicorexSecret string
	sessionSigner  *tokenSigner
	client         *http.Client
	// adminMounts add routes to the session-gated /_core/admin group from
	// packages that own their own handlers (the config store's screens).
	adminMounts []func(*gin.RouterGroup)
	// store serves plugin database config and operator commands. nil leaves
	// those endpoints answering 503 and heartbeats carrying neither, which is
	// what tests of the routing side need.
	store *store.Store
}

// SetStore connects the config store. Call it before Mount.
func (h *Handlers) SetStore(st *store.Store) { h.store = st }

// MountAdmin registers fn to add routes to the session-gated /_core/admin
// group. Call it before Mount.
func (h *Handlers) MountAdmin(fn func(*gin.RouterGroup)) { h.adminMounts = append(h.adminMounts, fn) }

// LoginEnabled reports whether the dashboard has a login at all. Without one
// the admin routes are open, which is tolerable for resetting a breaker on a
// dev machine and not for changing where plugins connect.
func (h *Handlers) LoginEnabled() bool { return h.apicorexSecret != "" }

// New builds the control-plane handlers. allowlist is the set of plugin names
// permitted to register (empty slice = allow any, for dev). The signer secret
// should be a strong secret (reuse JWT_SECRET or a dedicated one). apicorexSecret
// gates the gateway dashboard's login (a single shared key, not a
// username/password pair) — empty means login is disabled (dev only),
// matching the rest of Core's dev-mode posture.
func New(reg *registry.Registry, disp *dispatcher.Dispatcher, injector *openapi.Injector, apiKey string, allowlist []string, signerSecret, apicorexSecret string) *Handlers {
	al := make(map[string]bool, len(allowlist))
	for _, n := range allowlist {
		if n = strings.TrimSpace(n); n != "" {
			al[n] = true
		}
	}
	return &Handlers{
		reg:            reg,
		disp:           disp,
		injector:       injector,
		apiKey:         apiKey,
		allowlist:      al,
		signer:         newTokenSigner(signerSecret, 24*time.Hour),
		apicorexSecret: apicorexSecret,
		// Derived from apicorexSecret, not signerSecret: the two are independent
		// env vars (PLUGIN_API_KEY vs APICOREX_SECRET), and PLUGIN_API_KEY may
		// be unset in dev. If it were reused here, an unset PLUGIN_API_KEY would
		// make the HMAC key a fixed, source-visible string (":dashboard"),
		// letting anyone forge session tokens even with APICOREX_SECRET set.
		// When apicorexSecret is itself empty, requireSession no-ops regardless of
		// signature validity, so a weak/guessable key here is harmless.
		sessionSigner: newTokenSigner(apicorexSecret+":session", 12*time.Hour),
		client:        &http.Client{Timeout: 10 * time.Second},
	}
}

// Mount registers the /_core/* routes on the engine.
func (h *Handlers) Mount(engine *gin.Engine) {
	g := engine.Group("/_core")
	g.POST("/register", h.register)
	g.POST("/heartbeat", h.heartbeat)
	g.POST("/deregister", h.deregister)
	g.POST("/config/db", h.dbConfig)
	g.POST("/config/settings", h.settingsConfig)
	g.POST("/commands/:id/result", h.commandResult)
	g.GET("/plugins/:name/manifest", h.pluginManifest)
	// Maintenance windows. Authenticated with the same shared plugin key as
	// register/heartbeat: the caller is Identity, doing structural work on a
	// plugin's tables, not a person at a dashboard.
	g.POST("/maintenance", h.setMaintenance)
	g.GET("/maintenance", h.listMaintenance)

	// dashboard login — unauthenticated by definition (this is where a session
	// starts). /login-required lets the frontend know whether to show a login
	// form at all (dev instances with no APICOREX_SECRET set skip it).
	g.GET("/admin/login-required", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"required": h.apicorexSecret != ""})
	})
	g.POST("/admin/login", h.login)
	g.POST("/admin/logout", h.logout)

	// operator actions from the gateway dashboard — gated by a session token
	// from /admin/login. Login disabled (apicorexSecret == "") means these are
	// open, matching the rest of Core's dev-mode posture.
	admin := g.Group("/admin", h.requireSession)
	admin.GET("/session", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	admin.POST("/plugins/:id/reset-breaker", h.resetBreaker)
	admin.POST("/plugins/:id/deregister", h.adminDeregister)
	for _, fn := range h.adminMounts {
		fn(admin)
	}
}

// maintenanceReq opens or closes a window on one plugin.
type maintenanceReq struct {
	APIKey  string `json:"api_key"`
	Plugin  string `json:"plugin"`
	On      bool   `json:"on"`
	Reason  string `json:"reason"`
	Seconds int    `json:"seconds"`
}

// setMaintenance makes a plugin answer 503 without stopping it.
//
// Opening a window is how a structural migration keeps requests off the tables
// it is moving. Stopping the plugin would do the same and cost its
// registration — and with hot reload in play, a restart mid-migration would
// re-register and start serving again halfway through.
//
// A window always expires (see registry.maxMaintenance), so a caller that dies
// holding one leaves a plugin unavailable for minutes rather than forever.
func (h *Handlers) setMaintenance(c *gin.Context) {
	var req maintenanceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	who, ok := h.authenticate(c, req.APIKey)
	if !ok {
		return
	}
	if req.Plugin == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plugin required"})
		return
	}
	// With its own key a plugin may hold only itself off its tables; Identity
	// may hold any plugin, since it runs every plugin's structural migrations.
	if !who.shared && who.plugin != req.Plugin && who.plugin != identityPlugin {
		c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("%q may open a maintenance window only on itself", who.plugin)})
		return
	}
	if !req.On {
		h.reg.ClearMaintenance(req.Plugin)
		log.Printf("[controlplane] maintenance cleared for %s", req.Plugin)
		c.JSON(http.StatusOK, gin.H{"plugin": req.Plugin, "on": false})
		return
	}
	w := h.reg.SetMaintenance(req.Plugin, req.Reason, time.Duration(req.Seconds)*time.Second)
	log.Printf("[controlplane] maintenance on for %s until %s: %s",
		req.Plugin, w.Until.Format(time.RFC3339), req.Reason)
	c.JSON(http.StatusOK, gin.H{"plugin": req.Plugin, "on": true, "window": w})
}

// listMaintenance reports the open windows.
//
// Unauthenticated on purpose: it says only that a plugin is unavailable and
// until when, which is exactly what every 503 from it already says.
func (h *Handlers) listMaintenance(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"maintenance": h.reg.Maintenances()})
}

// sessionCookieName is set on login alongside the returned Bearer token: the
// dashboard SPA uses the token (via localStorage + Authorization header) for
// its own /admin/* calls, while the cookie lets browser-navigated pages like
// /docs (and Scalar's same-origin fetch of /docs/openapi.json) authenticate
// without any custom header.
const sessionCookieName = "apicorex_session"

// login validates the dashboard secret key against APICOREX_SECRET and
// issues a signed session token.
func (h *Handlers) login(c *gin.Context) {
	var req struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if h.apicorexSecret == "" {
		// login disabled (dev) — issue a token anyway so the frontend flow works
		token := h.sessionSigner.issue("dev")
		h.setSessionCookie(c, token)
		c.JSON(http.StatusOK, gin.H{"token": token})
		return
	}
	if subtle.ConstantTimeCompare([]byte(req.Key), []byte(h.apicorexSecret)) != 1 {
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
	if h.apicorexSecret == "" {
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
	if h.apicorexSecret == "" {
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
	who, ok := h.authenticate(c, req.APIKey)
	if !ok {
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

	// A plugin's own key registers that plugin and no other. With the shared
	// key anyone could register under any name — including identity, which
	// Core sends every token to — and this is what closes that.
	if !who.shared && who.plugin != m.Name {
		log.Printf("[controlplane] refused %q registering with %q's key from %s", m.Name, who.plugin, req.BaseURL)
		c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("this key belongs to %q, not %q", who.plugin, m.Name)})
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
	auth := "own"
	if who.shared {
		auth = "shared"
	}
	h.reg.SetAuth(pluginID, auth)
	h.disp.AddRoutes(pluginID, m.Name, m.PluginType, m.Routes)
	// Back from a dashboard restart: reopen its routes now rather than when
	// the window would have run out.
	h.reg.EndRestartWindow(m.Name)
	h.injector.AddRoutes(m.Name, m.Routes, m.OpenAPISpec)

	// issue a signed plugin token; plugin presents it on heartbeat/deregister
	token := h.signer.issue(pluginID)

	protection.PluginsRegistered.Set(float64(len(h.reg.List())))
	log.Printf("[controlplane] registered %s (%s) at %s — %d routes", m.Name, pluginID, req.BaseURL, len(m.Routes))
	c.JSON(http.StatusOK, gin.H{"plugin_id": pluginID, "plugin_token": token})
}

// heartbeat keeps a plugin registered, and is the channel Core talks back on.
//
// The plugin may report its database pool — db_source ("core" or "env") and
// db_version — so the dashboard can show whether a saved change has reached
// it. The response carries config_version, the plugin's current database
// config version, and at most one waiting command. A plugin that knows
// neither ignores both; Core can therefore ship this before any plugin does.
func (h *Handlers) heartbeat(c *gin.Context) {
	var req struct {
		PluginID    string `json:"plugin_id"`
		PluginToken string `json:"plugin_token"`
		DBSource    string `json:"db_source"`
		DBVersion   int64  `json:"db_version"`
		// Settings the plugin loaded at startup, and which keys its
		// environment overrides. Absent from a plugin built before settings.
		SettingsLoaded  bool     `json:"settings_loaded"`
		SettingsVersion int64    `json:"settings_version"`
		SettingsFromEnv []string `json:"settings_from_env"`
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
	resp := gin.H{"acknowledged": true}
	entry, ok := h.reg.Get(req.PluginID)
	if !ok || h.store == nil || store.ValidatePluginName(entry.Info.PluginName) != nil {
		c.JSON(http.StatusOK, resp)
		return
	}
	name := entry.Info.PluginName
	h.reg.ReportDB(req.PluginID, req.DBSource, req.DBVersion)
	h.reg.ReportSettings(req.PluginID, req.SettingsLoaded, req.SettingsVersion, req.SettingsFromEnv)

	ctx := c.Request.Context()
	// A store error must not fail the heartbeat: the plugin would read it as
	// Core being down and re-register, over a problem that is not about
	// registration at all. It just hears nothing new this time.
	if eff, err := h.store.ResolveDBConfig(ctx, name); err == nil {
		resp["config_version"] = eff.Version
	} else {
		log.Printf("[controlplane] heartbeat %s: config version: %v", name, err)
	}
	if v, err := h.store.SettingsVersion(ctx, name); err == nil {
		resp["settings_version"] = v
	} else {
		log.Printf("[controlplane] heartbeat %s: settings version: %v", name, err)
	}
	if cmd, err := h.store.TakeCommand(ctx, name); err != nil {
		log.Printf("[controlplane] heartbeat %s: command: %v", name, err)
	} else if cmd != nil {
		resp["command"] = gin.H{"id": cmd.ID, "kind": cmd.Kind}
		log.Printf("[controlplane] delivered %s command %d to %s", cmd.Kind, cmd.ID, req.PluginID)
	}
	c.JSON(http.StatusOK, resp)
}

// dbConfig hands a plugin the database connection the dashboard set for it.
//
// It is called before the plugin registers — the pool comes first — so it
// authenticates with the plugin API key and names the plugin in the body
// rather than presenting a plugin token. The key is shared by every plugin
// today, so this trusts the caller about which plugin it is; that is no worse
// than before, when every plugin was handed the same DSN anyway, but it is why
// per-plugin keys must come before per-plugin database roles mean anything.
func (h *Handlers) dbConfig(c *gin.Context) {
	var req struct {
		APIKey string `json:"api_key"`
		Plugin string `json:"plugin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	who, ok := h.authenticate(c, req.APIKey)
	if !ok {
		return
	}
	if req.Plugin, ok = h.pluginFor(c, who, req.Plugin); !ok {
		return
	}
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config store unavailable"})
		return
	}

	ctx := c.Request.Context()
	eff, err := h.store.EffectiveDBConfig(ctx, req.Plugin)
	switch {
	case errors.Is(err, store.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("no database connection is configured for %q — set one in the gateway dashboard", req.Plugin)})
		return
	case errors.Is(err, store.ErrNoMasterKey), errors.Is(err, store.ErrWrongKey):
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Core cannot open stored connections: " + err.Error()})
		return
	case err != nil:
		log.Printf("[controlplane] db config for %s: %v", req.Plugin, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config store error"})
		return
	}

	// Handing out a password is worth a line in the audit trail; the value
	// itself never goes there.
	if err := h.store.Audit(ctx, "plugin:"+req.Plugin+"@"+c.ClientIP(), "db_config.fetch", req.Plugin, fmt.Sprintf("version %d", eff.Version)); err != nil {
		log.Printf("[controlplane] audit db config fetch: %v", err)
	}
	c.JSON(http.StatusOK, gin.H{
		"dsn":                 eff.DSN,
		"max_open":            eff.MaxOpen,
		"max_idle":            eff.MaxIdle,
		"conn_max_lifetime_s": int(eff.ConnMaxLifetime.Seconds()),
		"conn_max_idle_s":     int(eff.ConnMaxIdleTime.Seconds()),
		"version":             eff.Version,
	})
}

// settingsConfig hands a plugin the settings set for it on the dashboard,
// with their version. Called before the plugin registers, like dbConfig, and
// authenticated the same way. 200 with an empty map when nothing is set: the
// plugin then runs on its environment and declared defaults, which is the
// normal state for a plugin nobody has configured here.
//
// Only values are sent, not the declarations: the plugin made those and
// knows them, and applying its own precedence (environment first) is its
// job, not Core's.
func (h *Handlers) settingsConfig(c *gin.Context) {
	var req struct {
		APIKey string `json:"api_key"`
		Plugin string `json:"plugin"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	who, ok := h.authenticate(c, req.APIKey)
	if !ok {
		return
	}
	if req.Plugin, ok = h.pluginFor(c, who, req.Plugin); !ok {
		return
	}
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config store unavailable"})
		return
	}
	ctx := c.Request.Context()
	set, err := h.store.ListSettings(ctx, req.Plugin)
	if err != nil {
		log.Printf("[controlplane] settings for %s: %v", req.Plugin, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config store error"})
		return
	}
	version, err := h.store.SettingsVersion(ctx, req.Plugin)
	if err != nil {
		log.Printf("[controlplane] settings version for %s: %v", req.Plugin, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config store error"})
		return
	}
	values := make(map[string]string, len(set))
	for k, v := range set {
		values[k] = v.Value
	}
	c.JSON(http.StatusOK, gin.H{"values": values, "version": version})
}

// commandResult records how a delivered command went. A restart reports
// before it exits — after that there is no process left to report — so its
// "done" means "on its way down"; the plugin re-registering is what shows it
// came back.
func (h *Handlers) commandResult(c *gin.Context) {
	var req struct {
		PluginID    string `json:"plugin_id"`
		PluginToken string `json:"plugin_token"`
		OK          bool   `json:"ok"`
		Message     string `json:"message"`
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
		c.JSON(http.StatusNotFound, gin.H{"error": "plugin not found"})
		return
	}
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config store unavailable"})
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid command id"})
		return
	}
	cmd, err := h.store.FinishCommand(c.Request.Context(), id, entry.Info.PluginName, req.OK, req.Message)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no delivered command with that id for this plugin"})
		return
	}
	if err != nil {
		log.Printf("[controlplane] command %d result: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config store error"})
		return
	}
	// A plugin restarting stays registered — deregistering would drop its
	// routes, and callers would get "no plugin handles this route" instead of
	// "back shortly". The window turns the gap into a 503 with Retry-After and
	// closes when the plugin registers again.
	if cmd.Kind == store.CommandRestart && req.OK {
		h.reg.SetRestartWindow(entry.Info.PluginName, restartWindow)
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// restartWindow bounds how long a restart may keep a plugin's routes in
// maintenance. Long enough for a container to stop, start and register; short
// enough that a plugin which never comes back stops being reported as merely
// restarting.
const restartWindow = 2 * time.Minute

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

// identityPlugin is the plugin Core already relies on by name for
// introspection and custom domains. Its key alone may open a maintenance
// window on another plugin: it does so around structural migrations.
const identityPlugin = "identity"

// caller is who presented an API key: a plugin by its own key, or anyone
// holding the shared PLUGIN_API_KEY.
type caller struct {
	plugin string // set for a plugin's own key
	shared bool
}

// authenticate identifies the caller by the API key it presented, writing the
// refusal itself when there is none. A plugin's own key names the plugin; the
// shared key names nobody, and is accepted only while the dashboard still
// allows it (and, as before, anything is accepted when no shared key is
// configured — dev).
func (h *Handlers) authenticate(c *gin.Context, key string) (caller, bool) {
	ctx := c.Request.Context()
	if h.store != nil && key != "" {
		p, ok, err := h.store.LookupPluginKey(ctx, key)
		if err != nil {
			log.Printf("[controlplane] key lookup: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "config store error"})
			return caller{}, false
		}
		if ok {
			return caller{plugin: p}, true
		}
		// Shaped like a key of Core's own and not found: revoked, or never
		// issued here. Say that, rather than fall through to the shared key
		// and send the operator looking at the wrong thing.
		if strings.HasPrefix(key, "akx_") {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "this plugin key is unknown or has been revoked; issue a new one on the dashboard (API keys)"})
			return caller{}, false
		}
	}
	if h.store != nil {
		accept, err := h.store.AcceptSharedKey(ctx)
		if err != nil {
			// Keep the default rather than lock every plugin out over a
			// read error; the error is logged where someone will look.
			log.Printf("[controlplane] reading accept-shared-key: %v", err)
			accept = true
		}
		if !accept {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "the shared PLUGIN_API_KEY is no longer accepted; give this plugin its own key (CORE_API_KEY) from the dashboard"})
			return caller{}, false
		}
	}
	if h.apiKey != "" && subtle.ConstantTimeCompare([]byte(key), []byte(h.apiKey)) != 1 {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid api key"})
		return caller{}, false
	}
	return caller{shared: true}, true
}

// pluginFor resolves which plugin a config request is about. A plugin's own
// key decides it, and naming another plugin in the body is refused rather than
// ignored — that is a misconfigured deploy, and saying so beats quietly
// handing over the right plugin's config to the wrong process. With the
// shared key the body is all there is, as before.
func (h *Handlers) pluginFor(c *gin.Context, who caller, named string) (string, bool) {
	if !who.shared {
		if named != "" && named != who.plugin {
			c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("this key belongs to %q, not %q", who.plugin, named)})
			return "", false
		}
		named = who.plugin
	}
	if len(h.allowlist) > 0 && !h.allowlist[named] {
		c.JSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("plugin %q not in allowlist", named)})
		return "", false
	}
	if named == store.DefaultPlugin || store.ValidatePluginName(named) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid plugin name"})
		return "", false
	}
	return named, true
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
