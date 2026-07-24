package protection

import (
	"errors"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("circuit breaker open")

type cbState int

const (
	stateClosed cbState = iota
	stateOpen
	stateHalfOpen
)

// CircuitBreaker trips per plugin after consecutive failures, briefly rejecting
// requests (open state) before letting a probe through (half-open) to test
// recovery. It is safe for concurrent use.
type CircuitBreaker struct {
	mu           sync.Mutex
	states       map[string]cbState
	failures     map[string]int
	lastFailure  map[string]time.Time
	threshold    int
	resetTimeout time.Duration
}

// NewCircuitBreaker returns a breaker that opens after threshold consecutive
// failures and stays open for resetTimeout before probing.
func NewCircuitBreaker(threshold int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		states:       make(map[string]cbState),
		failures:     make(map[string]int),
		lastFailure:  make(map[string]time.Time),
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
}

// Allow reports whether a request may proceed. It returns an error when the
// breaker is open; when half-open it lets a single probe through.
func (cb *CircuitBreaker) Allow(pluginID string) error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	state := cb.states[pluginID]
	switch state {
	case stateOpen:
		if time.Since(cb.lastFailure[pluginID]) > cb.resetTimeout {
			cb.states[pluginID] = stateHalfOpen
			return nil
		}
		return ErrCircuitOpen
	default:
		return nil
	}
}

// RecordSuccess resets the failure count and closes the breaker.
func (cb *CircuitBreaker) RecordSuccess(pluginID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures[pluginID] = 0
	cb.states[pluginID] = stateClosed
}

// RecordFailure increments the failure count and opens the breaker once the
// threshold is reached.
func (cb *CircuitBreaker) RecordFailure(pluginID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures[pluginID]++
	cb.lastFailure[pluginID] = time.Now()
	if cb.failures[pluginID] >= cb.threshold {
		cb.states[pluginID] = stateOpen
	}
}

// Reset manually closes the breaker and clears its failure count (an operator
// action from the gateway dashboard). If the plugin is still actually failing,
// the next failed request re-opens it as usual — this just gives it another
// chance immediately instead of waiting out resetTimeout.
func (cb *CircuitBreaker) Reset(pluginID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures[pluginID] = 0
	cb.states[pluginID] = stateClosed
}

// ForceOpen trips the breaker immediately (used by the health monitor).
func (cb *CircuitBreaker) ForceOpen(pluginID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.states[pluginID] = stateOpen
	cb.lastFailure[pluginID] = time.Now()
}

// IsOpen reports whether the breaker is currently open.
func (cb *CircuitBreaker) IsOpen(pluginID string) bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.states[pluginID] == stateOpen
}

// StateMetric returns the breaker state as a Prometheus-friendly value:
// 0=closed, 1=half-open, 2=open.
func (cb *CircuitBreaker) StateMetric(pluginID string) float64 {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.states[pluginID] {
	case stateOpen:
		return 2
	case stateHalfOpen:
		return 1
	default:
		return 0
	}
}

// State returns a human-readable breaker state for pluginID: "closed",
// "half-open", or "open". Used by the gateway dashboard.
func (cb *CircuitBreaker) State(pluginID string) string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	switch cb.states[pluginID] {
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	default:
		return "closed"
	}
}
