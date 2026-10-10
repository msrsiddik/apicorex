package adminapi

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/store"
)

type pluginKeysView struct {
	Plugin     string `json:"plugin"`
	Registered bool   `json:"registered"`
	// Auth is how the running plugin registered: "own", "shared", or ""
	// when it is not registered.
	Auth string            `json:"auth"`
	Keys []store.PluginKey `json:"keys"`
}

func (h *Handlers) listKeys(c *gin.Context) {
	ctx := c.Request.Context()
	keys, err := h.store.ListPluginKeys(ctx)
	if err != nil {
		fail(c, err)
		return
	}
	accept, err := h.store.AcceptSharedKey(ctx)
	if err != nil {
		fail(c, err)
		return
	}
	byPlugin := map[string]*pluginKeysView{}
	view := func(name string) *pluginKeysView {
		if v, ok := byPlugin[name]; ok {
			return v
		}
		v := &pluginKeysView{Plugin: name, Keys: []store.PluginKey{}}
		if auth, ok := h.reg.AuthByName(name); ok {
			v.Registered, v.Auth = true, auth
		}
		byPlugin[name] = v
		return v
	}
	for _, n := range h.reg.Names() {
		view(n)
	}
	for _, k := range keys {
		v := view(k.Plugin)
		v.Keys = append(v.Keys, k)
	}
	out := make([]*pluginKeysView, 0, len(byPlugin))
	for _, v := range byPlugin {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	c.JSON(http.StatusOK, gin.H{"accept_shared": accept, "plugins": out})
}

// issueKey creates a key for a plugin. The response is the only place the
// key ever appears; Core keeps its hash.
func (h *Handlers) issueKey(c *gin.Context) {
	raw, k, err := h.store.IssuePluginKey(c.Request.Context(), c.Param("plugin"), actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": raw, "issued": k})
}

func (h *Handlers) revokeKey(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid key id"})
		return
	}
	if err := h.store.RevokePluginKey(c.Request.Context(), c.Param("plugin"), id, actor(c)); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

// setSharedKey turns acceptance of the shared PLUGIN_API_KEY on or off.
// Turning it off while a registered plugin still uses it is refused unless
// forced: that plugin keeps running, and is locked out at its next restart.
func (h *Handlers) setSharedKey(c *gin.Context) {
	var req struct {
		Accept bool `json:"accept"`
		Force  bool `json:"force"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if !req.Accept && !req.Force {
		var still []string
		for _, n := range h.reg.Names() {
			if auth, ok := h.reg.AuthByName(n); ok && auth == "shared" {
				still = append(still, n)
			}
		}
		if len(still) > 0 {
			sort.Strings(still)
			c.JSON(http.StatusConflict, gin.H{
				"error":           "these plugins still use the shared key and would be locked out at their next restart",
				"still_on_shared": still,
			})
			return
		}
	}
	if err := h.store.SetAcceptSharedKey(c.Request.Context(), req.Accept, actor(c)); err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"accept_shared": req.Accept})
}
