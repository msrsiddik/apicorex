package protection

import (
	"errors"
	"sync"
)

var ErrBulkheadFull = errors.New("plugin at max concurrency")

// Bulkhead caps the number of concurrent in-flight requests per plugin so one
// slow plugin cannot exhaust the gateway. It is safe for concurrent use.
type Bulkhead struct {
	mu      sync.Mutex
	active  map[string]int
	maxConc int
	// limits holds a plugin's own limit, set when its routes are added.
	// A plugin without one uses maxConc.
	limits map[string]int
}

// NewBulkhead returns a Bulkhead allowing maxConcurrent in-flight requests per
// plugin.
func NewBulkhead(maxConcurrent int) *Bulkhead {
	return &Bulkhead{
		active:  make(map[string]int),
		maxConc: maxConcurrent,
		limits:  make(map[string]int),
	}
}

// Acquire reserves a concurrency slot for a plugin, returning ErrBulkheadFull if
// the plugin is already at its limit. A successful Acquire must be paired with a
// Release.
func (b *Bulkhead) Acquire(pluginID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active[pluginID] >= b.limitFor(pluginID) {
		return ErrBulkheadFull
	}
	b.active[pluginID]++
	return nil
}

// Release returns a previously acquired concurrency slot.
func (b *Bulkhead) Release(pluginID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active[pluginID] > 0 {
		b.active[pluginID]--
	}
}

// Active returns the current in-flight request count for a plugin. Used by
// the gateway dashboard.
func (b *Bulkhead) Active(pluginID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active[pluginID]
}

// Max returns the default per-plugin concurrency limit.
func (b *Bulkhead) Max() int {
	return b.maxConc
}

// Configure sets one plugin's concurrency limit. A limit of zero or less
// leaves it on the default. Slots already held are kept; a lower limit only
// refuses new ones.
func (b *Bulkhead) Configure(pluginID string, limit int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limit > 0 {
		b.limits[pluginID] = limit
	} else {
		delete(b.limits, pluginID)
	}
}

// Forget drops a plugin's own limit, when its routes are removed.
func (b *Bulkhead) Forget(pluginID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.limits, pluginID)
}

// MaxFor returns the concurrency limit in force for a plugin.
func (b *Bulkhead) MaxFor(pluginID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limitFor(pluginID)
}

// limitFor is MaxFor with b.mu held.
func (b *Bulkhead) limitFor(pluginID string) int {
	if l, ok := b.limits[pluginID]; ok {
		return l
	}
	return b.maxConc
}
