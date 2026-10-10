package adminapi

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/store"
)

// Protection is what the protection screen needs from the dispatcher.
// *dispatcher.Dispatcher is one.
type Protection interface {
	// RefreshLimits applies a plugin's limits as they now resolve, for its
	// next request; RefreshAllLimits does it for every plugin.
	RefreshLimits(pluginName string)
	RefreshAllLimits()
	// RunningLimits is what a plugin's instances run under now.
	RunningLimits(pluginName string) (config.Limits, bool)
}

// SetProtection connects the dispatcher. base is the layer under the store:
// Core's environment and CONFIG_FILE default. Without it the routes say
// protection limits are not managed here.
func (h *Handlers) SetProtection(base config.Limits, p Protection) {
	h.protBase, h.prot = base, p
}

func (h *Handlers) requireProtection(c *gin.Context) bool {
	if h.prot == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "protection limits are not managed from the dashboard on this deployment"})
		return false
	}
	return true
}

// limitsView is a set of limits with every field resolved.
type limitsView struct {
	RatePerSec       float64 `json:"rate_per_sec"`
	RateBurst        float64 `json:"rate_burst"`
	TenantRatePerSec float64 `json:"tenant_rate_per_sec"`
	TenantRateBurst  float64 `json:"tenant_rate_burst"`
	BulkheadMax      int     `json:"bulkhead_max"`
	CBThreshold      int     `json:"cb_threshold"`
	CBResetTimeoutMs int64   `json:"cb_reset_timeout_ms"`
	RequestTimeoutMs int64   `json:"request_timeout_ms"`
}

func limitsViewOf(l config.Limits) limitsView {
	return limitsView{
		RatePerSec: l.RatePerSec, RateBurst: l.RateBurst,
		TenantRatePerSec: l.TenantRatePerSec, TenantRateBurst: l.TenantRateBurst,
		BulkheadMax: l.BulkheadMax, CBThreshold: l.CBThreshold,
		CBResetTimeoutMs: l.CBResetTimeout.Milliseconds(), RequestTimeoutMs: l.RequestTimeout.Milliseconds(),
	}
}

type protectionView struct {
	Plugin     string            `json:"plugin"`
	Registered bool              `json:"registered"`
	Effective  limitsView        `json:"effective"`
	Source     map[string]string `json:"source"`
	// Running is what its instances run under now — the effective limits
	// after a public plugin's scaling. nil when it is not registered.
	Running *limitsView `json:"running"`
	Version int64       `json:"version"`
}

func (h *Handlers) protectionView(c *gin.Context, name string, registered bool) (protectionView, error) {
	eff, err := h.store.EffectiveProtection(c.Request.Context(), name, h.protBase)
	if err != nil {
		return protectionView{}, err
	}
	v := protectionView{Plugin: name, Registered: registered, Effective: limitsViewOf(eff.Limits), Source: eff.Source, Version: eff.Version}
	if name != store.DefaultPlugin {
		if l, ok := h.prot.RunningLimits(name); ok {
			r := limitsViewOf(l)
			v.Running = &r
		}
	}
	return v, nil
}

// listProtection returns the stored rows, the layer under them, and for the
// default and every plugin — registered, or with a row of its own — what it
// resolves to and what it is running.
func (h *Handlers) listProtection(c *gin.Context) {
	if !h.requireProtection(c) {
		return
	}
	rows, err := h.store.ListProtectionLimits(c.Request.Context())
	if err != nil {
		fail(c, err)
		return
	}
	registered := map[string]bool{}
	for _, n := range h.reg.Names() {
		registered[n] = true
	}
	names := map[string]bool{}
	for n := range registered {
		names[n] = true
	}
	for _, r := range rows {
		if r.Plugin != store.DefaultPlugin {
			names[r.Plugin] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)

	def, err := h.protectionView(c, store.DefaultPlugin, false)
	if err != nil {
		fail(c, err)
		return
	}
	plugins := make([]protectionView, 0, len(sorted))
	for _, n := range sorted {
		v, err := h.protectionView(c, n, registered[n])
		if err != nil {
			fail(c, err)
			return
		}
		plugins = append(plugins, v)
	}
	c.JSON(http.StatusOK, gin.H{
		"config":  limitsViewOf(h.protBase),
		"rows":    rows,
		"default": def,
		"plugins": plugins,
	})
}

// applied refreshes the dispatcher after a change to plugin's row and
// answers with what it resolves to now.
func (h *Handlers) applied(c *gin.Context, plugin string) {
	if plugin == store.DefaultPlugin {
		h.prot.RefreshAllLimits()
	} else {
		h.prot.RefreshLimits(plugin)
	}
	_, registered := h.prot.RunningLimits(plugin)
	v, err := h.protectionView(c, plugin, registered)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

func (h *Handlers) saveProtection(c *gin.Context) {
	if !h.requireProtection(c) {
		return
	}
	var req struct {
		Limits store.ProtectionFields `json:"limits"`
		Note   string                 `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	plugin := c.Param("plugin")
	if err := h.store.SaveProtectionLimits(c.Request.Context(), plugin, req.Limits, req.Note, actor(c)); err != nil {
		fail(c, err)
		return
	}
	h.applied(c, plugin)
}

func (h *Handlers) removeProtection(c *gin.Context) {
	if !h.requireProtection(c) {
		return
	}
	plugin := c.Param("plugin")
	if err := h.store.DeleteProtectionLimits(c.Request.Context(), plugin, actor(c)); err != nil {
		fail(c, err)
		return
	}
	h.applied(c, plugin)
}

func (h *Handlers) protectionHistory(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	v, err := h.store.ProtectionHistory(c.Request.Context(), c.Param("plugin"), limit)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, v)
}

func (h *Handlers) rollbackProtection(c *gin.Context) {
	if !h.requireProtection(c) {
		return
	}
	var req struct {
		Version int64 `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Version <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version required"})
		return
	}
	plugin := c.Param("plugin")
	if err := h.store.RollbackProtectionLimits(c.Request.Context(), plugin, req.Version, actor(c)); err != nil {
		fail(c, err)
		return
	}
	h.applied(c, plugin)
}
