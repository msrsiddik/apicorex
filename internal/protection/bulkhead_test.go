package protection

import (
	"errors"
	"testing"
)

func TestBulkhead_CapsConcurrency(t *testing.T) {
	bh := NewBulkhead(2)
	const id = "p1"

	if err := bh.Acquire(id); err != nil {
		t.Fatalf("slot 1 should be free: %v", err)
	}
	if err := bh.Acquire(id); err != nil {
		t.Fatalf("slot 2 should be free: %v", err)
	}
	if err := bh.Acquire(id); !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("slot 3 should be rejected, got %v", err)
	}
}

func TestBulkhead_ReleaseFreesSlot(t *testing.T) {
	bh := NewBulkhead(1)
	const id = "p1"

	bh.Acquire(id) //nolint:errcheck
	if err := bh.Acquire(id); !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("expected full, got %v", err)
	}
	bh.Release(id)
	if err := bh.Acquire(id); err != nil {
		t.Fatalf("after release a slot should be free, got %v", err)
	}
}

func TestBulkhead_PerPluginIsolation(t *testing.T) {
	bh := NewBulkhead(1)
	bh.Acquire("p1") //nolint:errcheck

	if err := bh.Acquire("p2"); err != nil {
		t.Fatalf("p2 has its own limit, got %v", err)
	}
}
