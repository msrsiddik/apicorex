package protection

import (
	"testing"
	"time"
)

func TestRateLimiter_AllowsUpToBurst(t *testing.T) {
	rl := NewRateLimiter(1, 5) // 1/s refill, burst 5
	const key = "p1"

	for i := 0; i < 5; i++ {
		if !rl.Allow(key) {
			t.Fatalf("request %d within burst should be allowed", i)
		}
	}
	if rl.Allow(key) {
		t.Fatal("request beyond burst should be rejected")
	}
}

func TestRateLimiter_RefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(100, 1) // 100/s refill, burst 1
	const key = "p1"

	if !rl.Allow(key) {
		t.Fatal("first request should be allowed")
	}
	if rl.Allow(key) {
		t.Fatal("immediate second request should be rejected")
	}

	time.Sleep(20 * time.Millisecond) // ~2 tokens refill at 100/s
	if !rl.Allow(key) {
		t.Fatal("after refill the request should be allowed")
	}
}

func TestRateLimiter_PerKeyIsolation(t *testing.T) {
	rl := NewRateLimiter(1, 1)

	if !rl.Allow("p1") {
		t.Fatal("p1 first should be allowed")
	}
	if !rl.Allow("p2") {
		t.Fatal("p2 has its own bucket and should be allowed")
	}
}

func TestRateLimiter_EvictIdle(t *testing.T) {
	rl := NewRateLimiter(1, 1)
	rl.Allow("stale")
	time.Sleep(20 * time.Millisecond)
	rl.Allow("fresh")

	rl.EvictIdle(10 * time.Millisecond)

	if _, ok := rl.lastSeen["stale"]; ok {
		t.Error("stale key should have been evicted")
	}
	if _, ok := rl.lastSeen["fresh"]; !ok {
		t.Error("fresh key should not have been evicted")
	}
	// an evicted key starts over with a full burst, exactly like one that
	// was never seen before.
	if !rl.Allow("stale") {
		t.Error("evicted key should get a fresh burst on its next request")
	}
}
