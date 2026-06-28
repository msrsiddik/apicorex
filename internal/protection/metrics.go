package protection

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for the gateway. Exposed at /metrics.
var (
	RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "apicorex_requests_total",
		Help: "Total proxied requests, by plugin, method, and status class.",
	}, []string{"plugin", "method", "status"})

	RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "apicorex_request_duration_seconds",
		Help:    "Proxied request latency in seconds, by plugin and method.",
		Buckets: prometheus.DefBuckets, // 0.005 .. 10s
	}, []string{"plugin", "method"})

	RequestsInFlight = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "apicorex_requests_in_flight",
		Help: "In-flight proxied requests per plugin.",
	}, []string{"plugin"})

	RequestsRejected = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "apicorex_requests_rejected_total",
		Help: "Requests rejected by a protection layer, by plugin and reason.",
	}, []string{"plugin", "reason"})

	CircuitBreakerState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "apicorex_circuit_breaker_state",
		Help: "Circuit breaker state per plugin (0=closed, 1=half-open, 2=open).",
	}, []string{"plugin"})

	PluginsRegistered = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "apicorex_plugins_registered",
		Help: "Number of currently registered plugins.",
	})
)

// StatusClass maps an HTTP status code to a low-cardinality metric label
// (2xx, 3xx, 4xx, 5xx, other) to keep metric series bounded.
func StatusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "other"
	}
}
