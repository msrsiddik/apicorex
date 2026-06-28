package dispatcher

import (
	"testing"

	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
)

func TestPatternMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		match         bool
	}{
		{"/hello", "/hello", true},
		{"/hello", "/world", false},
		{"/hello/:name", "/hello/bob", true},
		{"/hello/:name", "/hello", false}, // segment count differs
		{"/users/:id/posts", "/users/7/posts", true},
		{"/users/:id/posts", "/users/7/comments", false},
		{"/a/b", "/a/b/c", false},
	}
	for _, c := range cases {
		if got := patternMatch(c.pattern, c.path); got != c.match {
			t.Errorf("patternMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.match)
		}
	}
}

func TestMethodMatch(t *testing.T) {
	if !methodMatch("*", "GET") {
		t.Error(`"*" should match any method`)
	}
	if !methodMatch("GET", "GET") {
		t.Error("exact method should match")
	}
	if methodMatch("GET", "POST") {
		t.Error("different methods should not match")
	}
}

func newTestDispatcher() *Dispatcher {
	return New(
		registry.New(),
		protection.NewCircuitBreaker(5, 0),
		protection.NewBulkhead(10),
		config.Defaults(),
	)
}

func TestDispatcher_IsPublic(t *testing.T) {
	d := newTestDispatcher()
	d.AddRoutes("id1", "auth", "internal", []manifest.Route{
		{Method: "POST", Path: "/auth/login", Public: true},
		{Method: "GET", Path: "/me", Public: false},
	})

	if !d.IsPublic("POST", "/auth/login") {
		t.Error("/auth/login should be public")
	}
	if d.IsPublic("GET", "/me") {
		t.Error("/me should not be public")
	}
	if d.IsPublic("GET", "/nonexistent") {
		t.Error("unknown route should not be public")
	}
}

func TestDispatcher_RemoveRoutes(t *testing.T) {
	d := newTestDispatcher()
	d.AddRoutes("id1", "auth", "internal", []manifest.Route{
		{Method: "POST", Path: "/auth/login", Public: true},
	})
	d.RemoveRoutes("id1")

	if d.IsPublic("POST", "/auth/login") {
		t.Error("route should be gone after RemoveRoutes")
	}
}
