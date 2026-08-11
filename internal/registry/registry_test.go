package registry

import (
	"net/http/httputil"
	"net/url"
	"testing"

	"github.com/msrsiddik/apicorex/internal/manifest"
)

func noopProxy(*url.URL) *httputil.ReverseProxy { return &httputil.ReverseProxy{} }

func mustRegister(t *testing.T, r *Registry, id, name string, surfaces []manifest.DomainSurface) {
	t.Helper()
	if err := r.Register(id, name, "http://plugin:1234", "1.0.0", "internal",
		manifest.Manifest{DomainSurfaces: surfaces}, noopProxy); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
}

// A surface a registered plugin declared is found, with its path prefix.
func TestFindByDomainSurface(t *testing.T) {
	r := New()
	mustRegister(t, r, "id1", "schoolyze", []manifest.DomainSurface{
		{Surface: "portal", PathPrefix: "/school/portal"},
		{Surface: "website", PathPrefix: "/site"},
	})

	entry, prefix, ok := r.FindByDomainSurface("portal")
	if !ok {
		t.Fatal("expected to find the portal surface")
	}
	if prefix != "/school/portal" {
		t.Errorf("prefix = %q, want /school/portal", prefix)
	}
	if entry.Info.PluginName != "schoolyze" {
		t.Errorf("plugin = %q, want schoolyze", entry.Info.PluginName)
	}

	if _, _, ok := r.FindByDomainSurface("panel"); ok {
		t.Error("panel was never declared and should not resolve")
	}
}

// A dead plugin's surfaces don't resolve — routing to a plugin that's down
// would just produce a 503 further along; better to say "not found" here and
// let the ordinary path-based 404 stand.
func TestFindByDomainSurface_ExcludesDeadPlugins(t *testing.T) {
	r := New()
	mustRegister(t, r, "id1", "schoolyze", []manifest.DomainSurface{
		{Surface: "portal", PathPrefix: "/school/portal"},
	})
	r.MarkDead("id1")

	if _, _, ok := r.FindByDomainSurface("portal"); ok {
		t.Error("a dead plugin's domain surface should not resolve")
	}
}

// Two plugins may each declare their own surfaces without colliding.
func TestFindByDomainSurface_MultiplePlugins(t *testing.T) {
	r := New()
	mustRegister(t, r, "id1", "schoolyze", []manifest.DomainSurface{
		{Surface: "portal", PathPrefix: "/school/portal"},
	})
	mustRegister(t, r, "id2", "otherapp", []manifest.DomainSurface{
		{Surface: "storefront", PathPrefix: "/shop"},
	})

	if _, prefix, ok := r.FindByDomainSurface("storefront"); !ok || prefix != "/shop" {
		t.Errorf("storefront: ok=%v prefix=%q, want true /shop", ok, prefix)
	}
	if _, prefix, ok := r.FindByDomainSurface("portal"); !ok || prefix != "/school/portal" {
		t.Errorf("portal: ok=%v prefix=%q, want true /school/portal", ok, prefix)
	}
}
