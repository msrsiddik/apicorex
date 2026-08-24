package registry

import (
	"sync"
	"time"
)

// Maintenance mode: a plugin that is up but must not be reached.
//
// Structural work on a plugin's tables — moving them between schemas, a
// backfill that rewrites rows — is only safe with nothing serving requests
// against them. Stopping the plugin achieves that and costs its registration;
// this achieves it without, so the plugin stays registered, keeps heartbeating,
// and answers again the moment the window closes.
//
// The window always expires. A caller that opens one and then dies — the
// migration crashed, the operator's laptop closed — would otherwise leave a
// plugin unreachable with nothing left to close it, and that failure would look
// exactly like an outage while being entirely self-inflicted. An expiry turns
// the worst case into "unavailable for the next few minutes", which recovers on
// its own.

// maxMaintenance caps how long a window may be opened for.
//
// Fifteen minutes because a migration that needs longer needs planning, not a
// longer default — and because this is the amount of time a deployment can be
// down by accident before someone should be told rather than waiting.
const maxMaintenance = 15 * time.Minute

// Maintenance is one open window.
type Maintenance struct {
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
	Until  time.Time `json:"until"`
}

type maintenanceStore struct {
	mu sync.RWMutex
	// keyed by plugin NAME, not id: a window is opened for "schoolyze" before
	// anything is done to its tables, and must still apply if the plugin
	// restarts mid-migration and registers under a new id.
	windows map[string]Maintenance
}

func newMaintenanceStore() *maintenanceStore {
	return &maintenanceStore{windows: make(map[string]Maintenance)}
}

// SetMaintenance opens or extends a window, returning what was stored.
//
// A zero or excessive duration is clamped rather than refused: the caller is a
// migration runner, and failing its request would leave it choosing between
// proceeding unprotected and not proceeding at all.
func (r *Registry) SetMaintenance(pluginName, reason string, d time.Duration) Maintenance {
	if d <= 0 || d > maxMaintenance {
		d = maxMaintenance
	}
	now := time.Now()
	w := Maintenance{Reason: reason, Since: now, Until: now.Add(d)}
	r.maint.mu.Lock()
	defer r.maint.mu.Unlock()
	// Keep the original Since when extending, so a log or a dashboard shows how
	// long this has really been going on.
	if prev, ok := r.maint.windows[pluginName]; ok && time.Now().Before(prev.Until) {
		w.Since = prev.Since
	}
	r.maint.windows[pluginName] = w
	return w
}

// ClearMaintenance closes a window early, which is the normal ending.
func (r *Registry) ClearMaintenance(pluginName string) {
	r.maint.mu.Lock()
	defer r.maint.mu.Unlock()
	delete(r.maint.windows, pluginName)
}

// MaintenanceFor returns the open window for a plugin, if any.
//
// An expired window is treated as absent and dropped on the way past, so
// nothing accumulates and no sweeper is needed.
func (r *Registry) MaintenanceFor(pluginName string) (Maintenance, bool) {
	r.maint.mu.RLock()
	w, ok := r.maint.windows[pluginName]
	r.maint.mu.RUnlock()
	if !ok {
		return Maintenance{}, false
	}
	if time.Now().After(w.Until) {
		r.ClearMaintenance(pluginName)
		return Maintenance{}, false
	}
	return w, true
}

// Maintenances lists every open window, for a dashboard.
func (r *Registry) Maintenances() map[string]Maintenance {
	r.maint.mu.RLock()
	names := make([]string, 0, len(r.maint.windows))
	for n := range r.maint.windows {
		names = append(names, n)
	}
	r.maint.mu.RUnlock()

	out := make(map[string]Maintenance, len(names))
	for _, n := range names {
		if w, ok := r.MaintenanceFor(n); ok {
			out[n] = w
		}
	}
	return out
}
