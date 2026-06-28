package protection

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3, time.Minute)
	const id = "p1"

	// closed: allowed
	if err := cb.Allow(id); err != nil {
		t.Fatalf("fresh breaker should allow: %v", err)
	}

	// 3 failures trips it
	cb.RecordFailure(id)
	cb.RecordFailure(id)
	cb.RecordFailure(id)

	if err := cb.Allow(id); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("breaker should be open after threshold, got %v", err)
	}
}

func TestCircuitBreaker_SuccessResets(t *testing.T) {
	cb := NewCircuitBreaker(2, time.Minute)
	const id = "p1"

	cb.RecordFailure(id)
	cb.RecordSuccess(id) // reset before tripping

	cb.RecordFailure(id) // only 1 failure now
	if err := cb.Allow(id); err != nil {
		t.Fatalf("breaker should still be closed, got %v", err)
	}
}

func TestCircuitBreaker_HalfOpenAfterTimeout(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)
	const id = "p1"

	cb.RecordFailure(id) // opens
	if err := cb.Allow(id); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("expected open, got %v", err)
	}

	time.Sleep(20 * time.Millisecond) // past reset timeout

	// half-open: one probe allowed
	if err := cb.Allow(id); err != nil {
		t.Fatalf("expected half-open probe to be allowed, got %v", err)
	}
}

func TestCircuitBreaker_StateMetric(t *testing.T) {
	cb := NewCircuitBreaker(1, time.Minute)
	const id = "p1"

	if got := cb.StateMetric(id); got != 0 {
		t.Fatalf("closed should be 0, got %v", got)
	}
	cb.RecordFailure(id) // open
	if got := cb.StateMetric(id); got != 2 {
		t.Fatalf("open should be 2, got %v", got)
	}
}

func TestCircuitBreaker_PerPluginIsolation(t *testing.T) {
	cb := NewCircuitBreaker(1, time.Minute)
	cb.RecordFailure("p1") // trip p1 only

	if err := cb.Allow("p1"); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("p1 should be open")
	}
	if err := cb.Allow("p2"); err != nil {
		t.Fatalf("p2 should be unaffected, got %v", err)
	}
}
