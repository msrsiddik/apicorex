package adminapi

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/store"
)

// settingView is one declared setting with what the dashboard holds for it.
type settingView struct {
	manifest.Setting
	// Value is what the dashboard set, empty when it set nothing.
	Value     string     `json:"value"`
	Set       bool       `json:"set"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	UpdatedBy string     `json:"updated_by,omitempty"`
	// FromEnv says the plugin's environment overrides this key, so a value
	// set here does not take effect until the variable is removed.
	FromEnv bool `json:"from_env"`
}

type pluginSettingsView struct {
	Plugin     string        `json:"plugin"`
	Registered bool          `json:"registered"`
	Settings   []settingView `json:"settings"`
	// Undeclared are keys set here that the plugin no longer declares — a
	// setting it dropped. Shown so they can be cleared, not edited.
	Undeclared     []store.PluginSetting `json:"undeclared"`
	Version        int64                 `json:"version"`
	RunningVersion int64                 `json:"running_version"`
	// LoadsSettings says the plugin's build loads settings from Core; one
	// built before that cannot apply what is saved here however often it
	// restarts.
	LoadsSettings bool `json:"loads_settings"`
}

func (h *Handlers) listSettings(c *gin.Context) {
	ctx := c.Request.Context()
	out := []pluginSettingsView{}
	for _, name := range h.reg.Names() {
		if store.ValidatePluginName(name) != nil {
			continue
		}
		st, ok := h.reg.SettingsStateByName(name)
		running, fromEnv, decl := st.Version, st.FromEnv, st.Declared
		set, err := h.store.ListSettings(ctx, name)
		if err != nil {
			fail(c, err)
			return
		}
		version, err := h.store.SettingsVersion(ctx, name)
		if err != nil {
			fail(c, err)
			return
		}
		env := map[string]bool{}
		for _, k := range fromEnv {
			env[k] = true
		}
		v := pluginSettingsView{Plugin: name, Registered: ok, Settings: []settingView{}, Undeclared: []store.PluginSetting{},
			Version: version, RunningVersion: running, LoadsSettings: st.Loaded}
		declared := map[string]bool{}
		for _, d := range decl {
			declared[d.Key] = true
			sv := settingView{Setting: d, FromEnv: env[d.Key]}
			if ps, has := set[d.Key]; has {
				at := ps.UpdatedAt
				sv.Value, sv.Set, sv.UpdatedAt, sv.UpdatedBy = ps.Value, true, &at, ps.UpdatedBy
			}
			v.Settings = append(v.Settings, sv)
		}
		for k, ps := range set {
			if !declared[k] {
				v.Undeclared = append(v.Undeclared, ps)
			}
		}
		sort.Slice(v.Undeclared, func(i, j int) bool { return v.Undeclared[i].Key < v.Undeclared[j].Key })
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	c.JSON(http.StatusOK, out)
}

// saveSettings applies a form: a string sets a key, null or "" clears it.
//
// Validation needs the plugin's declaration, so the plugin must be registered
// right now. A key it no longer declares may only be cleared.
func (h *Handlers) saveSettings(c *gin.Context) {
	plugin := c.Param("plugin")
	var req struct {
		Values map[string]*string `json:"values"`
		Note   string             `json:"note"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	st, ok := h.reg.SettingsStateByName(plugin)
	decl := st.Declared
	if !ok {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("%s is not registered, so its settings and their types are not known; start it first", plugin)})
		return
	}
	byKey := map[string]manifest.Setting{}
	for _, d := range decl {
		byKey[d.Key] = d
	}
	changes := map[string]*string{}
	for k, v := range req.Values {
		if v != nil && *v == "" {
			v = nil
		}
		d, declared := byKey[k]
		switch {
		case !declared && v != nil:
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s does not declare a setting %s", plugin, k)})
			return
		case declared && d.Secret:
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s is a secret; secrets stay in the plugin's environment for now", k)})
			return
		case v != nil:
			if err := d.Validate(*v); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		changes[k] = v
	}
	version, err := h.store.SaveSettings(c.Request.Context(), plugin, changes, req.Note, actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": version})
}

func (h *Handlers) settingsHistory(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	hist, err := h.store.SettingsHistory(c.Request.Context(), c.Param("plugin"), limit)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, hist)
}
