// Package adminapi serves the gateway dashboard's config screens: plugin
// database connections, their history, and the audit trail. Every route sits
// behind the dashboard session (see controlplane), and nothing here ever sends
// a stored DSN back — only its redacted form.
package adminapi

import (
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/registry"
	"github.com/msrsiddik/apicorex/internal/store"
)

// Registry is what the handlers need from Core's plugin registry.
type Registry interface {
	// Names lists the plugins currently registered, so the database screen
	// can show a plugin that has no row of its own yet.
	Names() []string
	// DBStateByName is what a plugin last reported about its pool.
	DBStateByName(name string) (source string, version int64, ok bool)
	// SettingsStateByName is what a plugin last reported about its settings,
	// and the settings its manifest declares.
	SettingsStateByName(name string) (registry.SettingsState, bool)
	// AuthByName is how a registered plugin authenticated: "own" or "shared".
	AuthByName(name string) (string, bool)
}

// Handlers serves the admin config API.
type Handlers struct {
	store *store.Store
	reg   Registry
	// writable is false when the dashboard login is disabled. Reads still
	// work; changing where a plugin connects, or connecting to a host on
	// someone's say-so, needs a login in front of it.
	writable bool
	prober   Prober
}

// New builds the handlers.
func New(st *store.Store, reg Registry, writable bool, prober Prober) *Handlers {
	return &Handlers{store: st, reg: reg, writable: writable, prober: prober}
}

// Mount registers the routes on the dashboard's session-gated admin group.
func (h *Handlers) Mount(admin *gin.RouterGroup) {
	admin.GET("/db-config", h.listDBConfig)
	admin.GET("/db-config/:plugin/history", h.history)
	admin.GET("/postgres", h.postgres)
	admin.GET("/audit", h.audit)
	admin.GET("/commands/:plugin", h.listCommands)
	admin.GET("/settings", h.listSettings)
	admin.GET("/settings/:plugin/history", h.settingsHistory)
	admin.GET("/keys", h.listKeys)

	w := admin.Group("", h.requireWritable)
	w.PUT("/db-config/:plugin", h.save)
	w.DELETE("/db-config/:plugin", h.remove)
	w.POST("/db-config/:plugin/rollback", h.rollback)
	w.POST("/db-config/:plugin/test", h.test)
	w.POST("/commands/:plugin", h.queueCommand)
	w.PUT("/settings/:plugin", h.saveSettings)
	w.POST("/settings/:plugin/restore", h.restoreSetting)
	w.POST("/keys/:plugin", h.issueKey)
	w.DELETE("/keys/:plugin/:id", h.revokeKey)
	w.PUT("/shared-key", h.setSharedKey)
}

func (h *Handlers) requireWritable(c *gin.Context) {
	if !h.writable {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"error": "dashboard login is disabled (APICOREX_SECRET is not set), so config cannot be changed from here",
		})
		return
	}
	c.Next()
}

// actor names who made a change. The dashboard has one shared key, not
// accounts, so the address is the most there is to tell one session from
// another.
func actor(c *gin.Context) string { return "dashboard@" + c.ClientIP() }

// fail maps store errors onto status codes, keeping their messages: they say
// which field was wrong, or that the master key is missing, which is exactly
// what the operator needs to read.
func fail(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, store.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, store.ErrNoMasterKey), errors.Is(err, store.ErrWrongKey):
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

type poolView struct {
	MaxOpen            int `json:"max_open"`
	MaxIdle            int `json:"max_idle"`
	ConnMaxLifetimeSec int `json:"conn_max_lifetime_s"`
	ConnMaxIdleSec     int `json:"conn_max_idle_s"`
}

func viewOf(e store.EffectiveDBConfig) poolView {
	return poolView{
		MaxOpen:            e.MaxOpen,
		MaxIdle:            e.MaxIdle,
		ConnMaxLifetimeSec: int(e.ConnMaxLifetime.Seconds()),
		ConnMaxIdleSec:     int(e.ConnMaxIdleTime.Seconds()),
	}
}

type pluginView struct {
	Name       string `json:"name"`
	Registered bool   `json:"registered"`
	HasOwnRow  bool   `json:"has_own_row"`
	// DSNSource is "own", "default", or "" when no DSN is set anywhere.
	DSNSource string   `json:"dsn_source"`
	Effective poolView `json:"effective"`
	Version   int64    `json:"version"`
	// Running is what the plugin itself last reported: "core" at
	// RunningVersion, "env" (its own DATABASE_URL, which this screen cannot
	// change), or "" when it reports nothing or is not registered.
	Running        string `json:"running"`
	RunningVersion int64  `json:"running_version"`
}

func (h *Handlers) listDBConfig(c *gin.Context) {
	ctx := c.Request.Context()
	rows, err := h.store.ListDBConfigs(ctx)
	if err != nil {
		fail(c, err)
		return
	}

	registered := map[string]bool{}
	for _, n := range h.reg.Names() {
		registered[n] = true
	}
	own := map[string]bool{}
	names := map[string]bool{}
	for _, r := range rows {
		if r.Plugin != store.DefaultPlugin {
			own[r.Plugin] = true
			names[r.Plugin] = true
		}
	}
	for n := range registered {
		names[n] = true
	}

	plugins := []pluginView{}
	total := 0
	for n := range names {
		// A registered name Core would never store (one the manifest allowed
		// but the store's naming rule does not) is skipped rather than failing
		// the whole screen.
		if store.ValidatePluginName(n) != nil {
			continue
		}
		eff, err := h.store.ResolveDBConfig(ctx, n)
		if err != nil {
			fail(c, err)
			return
		}
		src := ""
		switch eff.DSNSource {
		case store.DefaultPlugin:
			src = "default"
		case n:
			src = "own"
		}
		running, runningVer, _ := h.reg.DBStateByName(n)
		plugins = append(plugins, pluginView{
			Name: n, Registered: registered[n], HasOwnRow: own[n],
			DSNSource: src, Effective: viewOf(eff), Version: eff.Version,
			Running: running, RunningVersion: runningVer,
		})
		// Only plugins that will actually open a pool count against the
		// budget; one with no DSN anywhere opens nothing.
		if src != "" {
			total += eff.MaxOpen
		}
	}
	sort.Slice(plugins, func(i, j int) bool { return plugins[i].Name < plugins[j].Name })

	c.JSON(http.StatusOK, gin.H{
		"has_master_key": h.store.HasMasterKey(),
		"writable":       h.writable,
		"rows":           rows,
		"plugins":        plugins,
		"total_max_open": total,
	})
}

func (h *Handlers) save(c *gin.Context) {
	var in store.DBConfigInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	cfg, err := h.store.SaveDBConfig(c.Request.Context(), c.Param("plugin"), in, actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, cfg)
}

func (h *Handlers) remove(c *gin.Context) {
	if err := h.store.DeleteDBConfig(c.Request.Context(), c.Param("plugin"), actor(c)); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func (h *Handlers) history(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	v, err := h.store.DBConfigHistory(c.Request.Context(), c.Param("plugin"), limit)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

func (h *Handlers) rollback(c *gin.Context) {
	var req struct {
		Version int64 `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Version <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version required"})
		return
	}
	cfg, err := h.store.RollbackDBConfig(c.Request.Context(), c.Param("plugin"), req.Version, actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// test checks a connection before it is saved: the DSN typed into the form
// when dsn_action is "set", otherwise what the plugin would connect with now.
//
// It runs from Core, which may not reach the same hosts a plugin does — a
// failure here is worth heeding, a success is not proof. The plugin's own
// check when it reloads is the one that counts.
func (h *Handlers) test(c *gin.Context) {
	var req struct {
		DSNAction string `json:"dsn_action"`
		DSN       string `json:"dsn"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	plugin := c.Param("plugin")
	if err := store.ValidatePluginName(plugin); err != nil {
		fail(c, err)
		return
	}

	dsn := req.DSN
	if req.DSNAction != store.DSNSet {
		eff, err := h.store.EffectiveDBConfig(c.Request.Context(), plugin)
		if err != nil {
			fail(c, err)
			return
		}
		dsn = eff.DSN
	} else if err := store.ValidateDSN(dsn); err != nil {
		fail(c, err)
		return
	}

	res, err := h.prober.Probe(c.Request.Context(), dsn)
	if err != nil {
		// 200 with ok=false: the request worked, the connection did not, and
		// the form shows the driver's message next to the field.
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": res})
}

// postgres reports the server's connection limits through the default DSN,
// for the budget on the database screen.
func (h *Handlers) postgres(c *gin.Context) {
	eff, err := h.store.EffectiveDBConfig(c.Request.Context(), store.DefaultPlugin)
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "no default connection string is set"})
		return
	}
	if err != nil {
		fail(c, err)
		return
	}
	res, err := h.prober.Probe(c.Request.Context(), eff.DSN)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "result": res})
}

func (h *Handlers) audit(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	before, _ := strconv.ParseInt(c.Query("before"), 10, 64)
	entries, err := h.store.ListAudit(c.Request.Context(), limit, before)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, entries)
}

// queueCommand sends a plugin a restart or reload on its next heartbeat. The
// plugin need not be registered right now — it collects the command when it
// is, within the store's expiry.
func (h *Handlers) queueCommand(c *gin.Context) {
	var req struct {
		Kind string `json:"kind"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	cmd, err := h.store.QueueCommand(c.Request.Context(), c.Param("plugin"), req.Kind, actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, cmd)
}

func (h *Handlers) listCommands(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	cmds, err := h.store.ListCommands(c.Request.Context(), c.Param("plugin"), limit)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, cmds)
}
