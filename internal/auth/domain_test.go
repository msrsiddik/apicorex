package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeRegistry satisfies baseURLLookup with a single fixed base URL, so tests
// don't need a real registry.Registry.
type fakeRegistry struct{ baseURL string }

func (f fakeRegistry) GetBaseURL(name string) (string, bool) {
	if name != "identity" {
		return "", false
	}
	return f.baseURL, true
}

// A verified claim resolves; the plugin key is checked the same way
// Identity's own handler does.
func TestDomainResolver_Resolves(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Plugin-Key") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var in resolveDomainRequest
		json.NewDecoder(r.Body).Decode(&in)
		if in.Hostname != "portal.acme.com" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(ResolvedDomain{
			TenantID: "t_1", TenantSlug: "acme", SchemaName: "tenant_acme",
			BranchID: "br_1", Surface: "portal",
		})
	}))
	defer srv.Close()

	r := NewDomainResolver(fakeRegistry{baseURL: srv.URL}, "secret")
	res, err := r.Resolve(t.Context(), "portal.acme.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if res.TenantSlug != "acme" || res.Surface != "portal" {
		t.Errorf("got %+v", res)
	}
}

// An unclaimed hostname returns ErrDomainNotFound, not a generic error — the
// caller (server.resolveCustomDomain) treats this as "fall through to the
// ordinary 404", not a failure worth logging.
func TestDomainResolver_Unknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	r := NewDomainResolver(fakeRegistry{baseURL: srv.URL}, "secret")
	if _, err := r.Resolve(t.Context(), "nope.example.com"); err != ErrDomainNotFound {
		t.Errorf("err = %v, want ErrDomainNotFound", err)
	}
}

// Identity being unreachable is ErrUnavailable, distinct from "not found" —
// and, per Resolve's contract, never cached (a transient outage must not
// pin every request to "unavailable" for the full cache TTL).
func TestDomainResolver_UnavailableIsNotCached(t *testing.T) {
	calls := 0
	r := NewDomainResolver(fakeRegistry{baseURL: "http://127.0.0.1:1"}, "secret") // nothing listens here
	_ = calls
	if _, err := r.Resolve(t.Context(), "portal.acme.com"); err != ErrUnavailable {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	// A second call must still attempt the network, not serve a cached
	// ErrUnavailable — verified indirectly: the cache map must stay empty.
	r.mu.Lock()
	n := len(r.cache)
	r.mu.Unlock()
	if n != 0 {
		t.Errorf("cache has %d entries, want 0 — ErrUnavailable must not be cached", n)
	}
}

// A hostname is matched case-insensitively and independent of surrounding
// whitespace — Host headers and admin-entered hostnames are not guaranteed
// to be clean.
func TestDomainResolver_NormalizesHostname(t *testing.T) {
	var gotHostname string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in resolveDomainRequest
		json.NewDecoder(r.Body).Decode(&in)
		gotHostname = in.Hostname
		json.NewEncoder(w).Encode(ResolvedDomain{TenantSlug: "acme", Surface: "portal"})
	}))
	defer srv.Close()

	r := NewDomainResolver(fakeRegistry{baseURL: srv.URL}, "secret")
	if _, err := r.Resolve(t.Context(), "  Portal.ACME.com  "); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if gotHostname != "portal.acme.com" {
		t.Errorf("hostname sent = %q, want lowercased/trimmed", gotHostname)
	}
}
