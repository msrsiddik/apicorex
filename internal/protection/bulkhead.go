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
}

// NewBulkhead returns a Bulkhead allowing maxConcurrent in-flight requests per
// plugin.
func NewBulkhead(maxConcurrent int) *Bulkhead {
	return &Bulkhead{
		active:  make(map[string]int),
		maxConc: maxConcurrent,
	}
}

// Acquire reserves a concurrency slot for a plugin, returning ErrBulkheadFull if
// the plugin is already at its limit. A successful Acquire must be paired with a
// Release.
func (b *Bulkhead) Acquire(pluginID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active[pluginID] >= b.maxConc {
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

// Max returns the configured per-plugin concurrency limit.
func (b *Bulkhead) Max() int {
	return b.maxConc
}
