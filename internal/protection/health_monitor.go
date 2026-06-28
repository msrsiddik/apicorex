package protection

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/msrsiddik/apicorex/internal/registry"
)

// HealthMonitor checks all registered plugins every interval via HTTP.
type HealthMonitor struct {
	reg      *registry.Registry
	cb       *CircuitBreaker
	interval time.Duration
	client   *http.Client
}

// NewHealthMonitor returns a monitor that checks every plugin's
// /_apicorex/health endpoint at the given interval.
func NewHealthMonitor(reg *registry.Registry, cb *CircuitBreaker, interval time.Duration) *HealthMonitor {
	return &HealthMonitor{
		reg:      reg,
		cb:       cb,
		interval: interval,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

// Run blocks, checking plugin health on each tick until ctx is cancelled.
// Unhealthy plugins are marked dead and their circuit breaker is forced open;
// they recover automatically when health returns.
func (hm *HealthMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(hm.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hm.checkAll(ctx)
		}
	}
}

func (hm *HealthMonitor) checkAll(ctx context.Context) {
	for _, entry := range hm.reg.List() {
		pluginID := entry.Info.PluginID
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, entry.BaseURL+"/_apicorex/health", nil)
		resp, err := hm.client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			hm.reg.MarkDead(pluginID)
			hm.cb.ForceOpen(pluginID)
			log.Printf("[health] plugin %s unhealthy: %v", pluginID, err)
		} else {
			hm.reg.Heartbeat(pluginID) //nolint:errcheck
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
}
