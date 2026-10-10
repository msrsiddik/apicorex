// Package registry is the in-memory store of registered plugins. Nothing here
// is persisted (the config store, internal/store, is separate); plugins live
// here only while running. Each entry holds the plugin's
// manifest, target URL, and reverse proxy. The package is the source of truth
// for routing and the /plugins listing.
package registry

import (
	"fmt"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/msrsiddik/apicorex/internal/manifest"
)

// PluginInfo is the public summary of a registered plugin (exposed at /plugins).
type PluginInfo struct {
	PluginID   string
	PluginName string
	Version    string
	BaseURL    string
	Status     string // "healthy" | "unhealthy"
	Routes     int
}

// PluginEntry is a registered plugin's full in-memory record, including its
// manifest, parsed target URL, and the reverse proxy used to forward requests.
type PluginEntry struct {
	Info          PluginInfo
	Manifest      manifest.Manifest
	BaseURL       string
	Target        *url.URL
	Proxy         *httputil.ReverseProxy
	RegisteredAt  time.Time
	LastHeartbeat time.Time
	Alive         bool
	// DBSource and DBVersion are what the plugin last reported, on its
	// heartbeat, about its own database pool: "core" (config fetched from
	// Core, at DBVersion), "env" (DATABASE_URL in its environment, which the
	// dashboard cannot change), or "" for a plugin that says nothing — one
	// without a database, or built before plugins reported it.
	DBSource  string
	DBVersion int64
	// SettingsVersion and SettingsFromEnv are what the plugin reported about
	// its dashboard settings: the version it loaded at startup, and the keys
	// its environment overrides. Zero and nil for a plugin that says nothing.
	SettingsVersion int64
	SettingsFromEnv []string
	// SettingsLoaded is whether the plugin loads settings from Core at all.
	// Without it a version of 0 could mean "loaded when nothing was set" or
	// "built before settings", and the dashboard must tell those apart to say
	// "restart to apply" rather than "this build cannot".
	SettingsLoaded bool
	// Auth is how the plugin authenticated when it registered: "own" for a
	// key of its own, "shared" for the shared PLUGIN_API_KEY. The dashboard
	// shows it so the shared key is turned off only once nobody needs it.
	Auth string
}

// Registry is the in-memory store of registered plugins, keyed by plugin ID.
// It is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]*PluginEntry // keyed by plugin_id
	// maint holds the plugins that are up but must not be reached, keyed by
	// name rather than id. See maintenance.go.
	maint *maintenanceStore
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{plugins: make(map[string]*PluginEntry), maint: newMaintenanceStore()}
}

// Register stores a plugin and builds its reverse proxy. proxyFor is a factory
// that builds the *httputil.ReverseProxy (the dispatcher provides it so the
// proxy carries the shared transport + header injection director).
func (r *Registry) Register(pluginID, name, baseURL, version, pluginType string, m manifest.Manifest, proxyFor func(*url.URL) *httputil.ReverseProxy) error {
	target, err := url.Parse(baseURL)
	if err != nil {
		return fmt.Errorf("invalid base_url %q: %w", baseURL, err)
	}

	entry := &PluginEntry{
		Info: PluginInfo{
			PluginID:   pluginID,
			PluginName: name,
			Version:    version,
			BaseURL:    baseURL,
			Status:     "healthy",
			Routes:     len(m.Routes),
		},
		Manifest:      m,
		BaseURL:       baseURL,
		Target:        target,
		Proxy:         proxyFor(target),
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
		Alive:         true,
	}

	r.mu.Lock()
	r.plugins[pluginID] = entry
	r.mu.Unlock()
	return nil
}

// Heartbeat marks a plugin alive and refreshes its last-seen time.
func (r *Registry) Heartbeat(pluginID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.plugins[pluginID]
	if !ok {
		return fmt.Errorf("plugin %s not found", pluginID)
	}
	entry.LastHeartbeat = time.Now()
	entry.Alive = true
	entry.Info.Status = "healthy"
	return nil
}

// ReportDB records what a plugin said about its database pool on a heartbeat.
func (r *Registry) ReportDB(pluginID, source string, version int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.plugins[pluginID]; ok {
		entry.DBSource = source
		entry.DBVersion = version
	}
}

// SetAuth records how a plugin authenticated at registration.
func (r *Registry) SetAuth(pluginID, auth string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.plugins[pluginID]; ok {
		entry.Auth = auth
	}
}

// AuthByName returns how a registered plugin authenticated.
func (r *Registry) AuthByName(name string) (string, bool) {
	e, found := r.FindByName(name)
	if !found {
		return "", false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return e.Auth, true
}

// ReportSettings records what a plugin said about its settings on a
// heartbeat.
func (r *Registry) ReportSettings(pluginID string, loaded bool, version int64, fromEnv []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.plugins[pluginID]; ok {
		entry.SettingsLoaded = loaded
		entry.SettingsVersion = version
		entry.SettingsFromEnv = append([]string(nil), fromEnv...)
	}
}

// SettingsStateByName returns what a registered plugin last reported about
// its settings, and its manifest's declarations. ok is false when no plugin
// of that name is registered.
func (r *Registry) SettingsStateByName(name string) (SettingsState, bool) {
	e, found := r.FindByName(name)
	if !found {
		return SettingsState{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return SettingsState{
		Loaded:   e.SettingsLoaded,
		Version:  e.SettingsVersion,
		FromEnv:  append([]string(nil), e.SettingsFromEnv...),
		Declared: e.Manifest.Settings,
	}, true
}

// SettingsState is what the dashboard knows about a plugin's settings.
type SettingsState struct {
	Loaded   bool
	Version  int64
	FromEnv  []string
	Declared []manifest.Setting
}

// DBStateByName returns what a registered plugin last reported about its
// database pool. ok is false when no plugin of that name is registered.
func (r *Registry) DBStateByName(name string) (source string, version int64, ok bool) {
	e, found := r.FindByName(name)
	if !found {
		return "", 0, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return e.DBSource, e.DBVersion, true
}

// Deregister removes a plugin from the registry.
func (r *Registry) Deregister(pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.plugins, pluginID)
}

// MarkDead flags a plugin as unhealthy (set by the health monitor on a failed
// health check). The plugin stays registered and can recover via Heartbeat.
func (r *Registry) MarkDead(pluginID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if entry, ok := r.plugins[pluginID]; ok {
		entry.Alive = false
		entry.Info.Status = "unhealthy"
	}
}

// Get returns a plugin entry by ID.
func (r *Registry) Get(pluginID string) (*PluginEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.plugins[pluginID]
	return e, ok
}

// List returns all registered plugin entries.
func (r *Registry) List() []*PluginEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries := make([]*PluginEntry, 0, len(r.plugins))
	for _, e := range r.plugins {
		entries = append(entries, e)
	}
	return entries
}

// FindByName returns the first live (alive) plugin with the given name.
func (r *Registry) FindByName(name string) (*PluginEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.plugins {
		if e.Info.PluginName == name && e.Alive {
			return e, true
		}
	}
	return nil, false
}

// Names returns the distinct names of every registered plugin.
func (r *Registry) Names() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range r.List() {
		if n := e.Info.PluginName; !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// IDsByName returns the IDs of all registered plugins with the given name.
// Used to evict stale entries when a plugin re-registers (e.g. after a restart).
func (r *Registry) IDsByName(name string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var ids []string
	for id, e := range r.plugins {
		if e.Info.PluginName == name {
			ids = append(ids, id)
		}
	}
	return ids
}

// GetBaseURL returns the HTTP base URL of a live plugin by name.
func (r *Registry) GetBaseURL(name string) (string, bool) {
	e, ok := r.FindByName(name)
	if !ok {
		return "", false
	}
	return e.BaseURL, true
}

// GetManifest returns the stored manifest of a plugin by name (Identity uses
// this to pull migrations).
func (r *Registry) GetManifest(name string) (manifest.Manifest, bool) {
	e, ok := r.FindByName(name)
	if !ok {
		return manifest.Manifest{}, false
	}
	return e.Manifest, true
}

// FindByDomainSurface returns the live plugin that declared the given domain
// surface (see manifest.DomainSurface) and the declaration itself — the path
// prefix to rewrite a resolved custom-domain request to, and whether that
// surface takes its tenant from the session rather than the URL. Only ever consulted
// for a request whose Host resolved to a tenant but whose path matched no
// ordinary route — see dispatcher.resolveByHost.
func (r *Registry) FindByDomainSurface(surface string) (entry *PluginEntry, declared manifest.DomainSurface, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.plugins {
		if !e.Alive {
			continue
		}
		for _, s := range e.Manifest.DomainSurfaces {
			if s.Surface == surface {
				return e, s, true
			}
		}
	}
	return nil, manifest.DomainSurface{}, false
}
