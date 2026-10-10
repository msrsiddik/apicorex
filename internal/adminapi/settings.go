package adminapi

import (
	"encoding/json"
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

// declarations returns what plugin declares: from the running plugin when
// it is registered, else as last recorded at a registration. ok is false for
// a plugin Core has never seen register.
func (h *Handlers) declarations(c *gin.Context, plugin string) (decl []manifest.Setting, registered, ok bool, err error) {
	if st, reg := h.reg.SettingsStateByName(plugin); reg {
		return st.Declared, true, true, nil
	}
	raw, _, seen, err := h.store.Declarations(c.Request.Context(), plugin)
	if err != nil || !seen {
		return nil, false, false, err
	}
	if err := json.Unmarshal(raw, &decl); err != nil {
		return nil, false, false, err
	}
	return decl, false, true, nil
}

func (h *Handlers) listSettings(c *gin.Context) {
	ctx := c.Request.Context()
	out := []pluginSettingsView{}
	names := map[string]bool{}
	for _, n := range h.reg.Names() {
		names[n] = true
	}
	// A plugin that is down is listed too, from its last declarations: it is
	// often the one whose settings need changing.
	known, err := h.store.DeclaredPlugins(ctx)
	if err != nil {
		fail(c, err)
		return
	}
	for _, n := range known {
		names[n] = true
	}
	for name := range names {
		if store.ValidatePluginName(name) != nil {
			continue
		}
		st, _ := h.reg.SettingsStateByName(name)
		decl, ok, _, err := h.declarations(c, name)
		if err != nil {
			fail(c, err)
			return
		}
		running, fromEnv := st.Version, st.FromEnv
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
		// Confirm names each set-once key this save replaces or clears.
		Confirm []string `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	decl, _, known, err := h.declarations(c, plugin)
	if err != nil {
		fail(c, err)
		return
	}
	if !known {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("%s has never registered, so its settings and their types are not known; start it once first", plugin)})
		return
	}
	byKey := map[string]manifest.Setting{}
	for _, d := range decl {
		byKey[d.Key] = d
	}
	current, err := h.store.ListSettings(c.Request.Context(), plugin)
	if err != nil {
		fail(c, err)
		return
	}
	confirmed := map[string]bool{}
	for _, k := range req.Confirm {
		confirmed[k] = true
	}
	changes := map[string]*string{}
	secret := map[string]bool{}
	for k, v := range req.Values {
		if v != nil && *v == "" {
			v = nil
		}
		d, declared := byKey[k]
		switch {
		case !declared && v != nil:
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("%s does not declare a setting %s", plugin, k)})
			return
		case v != nil:
			if err := d.Validate(*v); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		// Replacing or clearing a set-once value breaks what depends on it —
		// stored passwords sealed with an encryption key, sessions signed with
		// a signing key — so it is done only when asked for by name.
		if _, isSet := current[k]; declared && d.SetOnce && isSet && !confirmed[k] {
			c.JSON(http.StatusConflict, gin.H{
				"error":       fmt.Sprintf("%s is set-once: replacing or clearing it breaks what was encrypted or signed with it. Confirm it by name to go ahead.", k),
				"set_once":    k,
				"description": d.Description,
			})
			return
		}
		changes[k] = v
		secret[k] = declared && d.Secret
	}
	version, err := h.store.SaveSettingsWithSecrets(c.Request.Context(), plugin, changes, secret, req.Note, actor(c))
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

// restoreSetting makes an earlier version of one key current again — a
// secret included, whose sealed value is copied without being opened. It is
// how a set-once value replaced by mistake is put back.
func (h *Handlers) restoreSetting(c *gin.Context) {
	var req struct {
		Version int64 `json:"version"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Version <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "version required"})
		return
	}
	version, err := h.store.RestoreSetting(c.Request.Context(), c.Param("plugin"), req.Version, actor(c))
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"version": version})
}
