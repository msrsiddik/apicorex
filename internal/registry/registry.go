// Package registry is the in-memory store of registered plugins. Core keeps no
// database; plugins live here only while running. Each entry holds the plugin's
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
}

// Registry is the in-memory store of registered plugins, keyed by plugin ID.
// It is safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	plugins map[string]*PluginEntry // keyed by plugin_id
}

// New returns an empty Registry.
func New() *Registry {
	return &Registry{plugins: make(map[string]*PluginEntry)}
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
