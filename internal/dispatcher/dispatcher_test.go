package dispatcher

import (
	"testing"

	"github.com/msrsiddik/apicorex/internal/auth"
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

// A route's declared permission is carried into the dispatch table so the
// authz check in Dispatch can enforce it.
func TestDispatcher_RoutePermission(t *testing.T) {
	d := newTestDispatcher()
	d.AddRoutes("id1", "identity", "internal", []manifest.Route{
		{Method: "POST", Path: "/branches", Public: false, Permission: "branch:write"},
		{Method: "GET", Path: "/branches", Public: false}, // no permission
	})

	if e := d.match("POST", "/branches"); e == nil || e.permission != "branch:write" {
		t.Errorf("POST /branches permission = %v, want branch:write", e)
	}
	if e := d.match("GET", "/branches"); e == nil || e.permission != "" {
		t.Errorf("GET /branches should carry no permission, got %q", e.permission)
	}
}

// A platform admin is authorized on any permission-declaring route even with
// no (or unrelated) tenant permissions — they act across tenants by design and
// may hold no tenant role at all.
func TestAuthorized_PlatformAdminBypasses(t *testing.T) {
	entry := &routeEntry{permission: "plugin:install"}

	admin := &auth.Claims{UserType: "platform_admin"} // no Permissions at all
	if !authorized(admin, entry) {
		t.Error("platform admin should be authorized regardless of permissions")
	}
}

// A regular tenant user needs the matching (or wildcard) permission; lacking
// it, or being unauthenticated, is rejected.
func TestAuthorized_TenantUser(t *testing.T) {
	entry := &routeEntry{permission: "plugin:install"}

	withPerm := &auth.Claims{UserType: "tenant_user", Permissions: []string{"plugin:install"}}
	if !authorized(withPerm, entry) {
		t.Error("tenant user with the exact permission should be authorized")
	}

	withWildcard := &auth.Claims{UserType: "tenant_user", Permissions: []string{"plugin:*"}}
	if !authorized(withWildcard, entry) {
		t.Error("tenant user with a wildcard permission should be authorized")
	}

	withoutPerm := &auth.Claims{UserType: "tenant_user", Permissions: []string{"branch:read"}}
	if authorized(withoutPerm, entry) {
		t.Error("tenant user without the permission should not be authorized")
	}

	if authorized(nil, entry) {
		t.Error("nil claims (unauthenticated) should not be authorized")
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
