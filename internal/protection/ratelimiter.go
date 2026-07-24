package protection

import (
	"sync"
	"time"
)

// RateLimiter is a token-bucket limiter keyed by an arbitrary string (Core uses
// the plugin ID). It is safe for concurrent use.
type RateLimiter struct {
	mu       sync.Mutex
	tokens   map[string]float64
	lastSeen map[string]time.Time
	rate     float64 // tokens per second
	burst    float64
}

// NewRateLimiter returns a limiter that refills ratePerSec tokens per second up
// to a maximum of burst.
func NewRateLimiter(ratePerSec, burst float64) *RateLimiter {
	return &RateLimiter{
		tokens:   make(map[string]float64),
		lastSeen: make(map[string]time.Time),
		rate:     ratePerSec,
		burst:    burst,
	}
}

// Allow consumes one token for key, returning true if a token was available.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	last, ok := r.lastSeen[key]
	if !ok {
		r.tokens[key] = r.burst
		r.lastSeen[key] = now
	} else {
		elapsed := now.Sub(last).Seconds()
		r.tokens[key] = min(r.burst, r.tokens[key]+elapsed*r.rate)
		r.lastSeen[key] = now
	}

	if r.tokens[key] >= 1 {
		r.tokens[key]--
		return true
	}
	return false
}

func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// Tokens returns the current token count for key without consuming one — a
// read-only peek for the gateway dashboard.
func (r *RateLimiter) Tokens(key string) float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.tokens[key]; ok {
		return t
	}
	return r.burst
}

// Burst returns the configured burst capacity.
func (r *RateLimiter) Burst() float64 {
	return r.burst
}
