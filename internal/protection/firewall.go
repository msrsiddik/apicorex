// Package protection implements the gateway's resilience and observability
// layers applied to every proxied request: a route firewall, per-plugin rate
// limiter, bulkhead (concurrency limit), circuit breaker, a background health
// monitor, Prometheus metrics, and structured request logging.
package protection

import (
	"strings"
)

var blockedPrefixes = []string{"/internal/", "/admin/", "/_core/"}

const maxBodySize = 10 * 1024 * 1024 // 10MB

// IsRouteBlocked reports whether a path targets a reserved, externally-blocked
// prefix (e.g. /_core/, /admin/). The control plane is reachable plugin→Core but
// not through the public data path.
func IsRouteBlocked(path string) bool {
	for _, prefix := range blockedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// IsBodyTooLarge reports whether a request body exceeds the 10MB limit.
func IsBodyTooLarge(size int64) bool {
	return size > maxBodySize
}
