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
