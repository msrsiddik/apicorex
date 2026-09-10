package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/msrsiddik/apicorex/internal/auth"
)

// identityStub stands in for Identity's /internal/resolve-domain, answering for
// exactly one verified hostname the way the real one does.
func identityStub(t *testing.T, verified string) *auth.DomainResolver {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Hostname string `json:"hostname"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Hostname != verified {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(auth.ResolvedDomain{
			TenantID: "t_1", TenantSlug: "abc", SchemaName: "tenant_abc", Surface: "website",
		})
	}))
	t.Cleanup(srv.Close)
	return auth.NewDomainResolver(fakeBaseURLLookup{srv.URL}, "secret")
}

func ask(t *testing.T, resolver *auth.DomainResolver, query string) int {
	t.Helper()
	w := httptest.NewRecorder()
	askHandler(resolver)(w, httptest.NewRequest(http.MethodGet, OnDemandTLSPath+query, nil))
	return w.Code
}

func TestAskHandler(t *testing.T) {
	resolver := identityStub(t, "abc.example.com")

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"a hostname a tenant verified", "?domain=abc.example.com", http.StatusOK},
		// The whole reason the gate exists: anyone can point DNS here, and
		// every one of those must be refused before a certificate is requested.
		{"a hostname nobody claimed", "?domain=squatter.example.com", http.StatusNotFound},
		{"no domain at all", "", http.StatusBadRequest},
		{"an empty domain", "?domain=", http.StatusBadRequest},
		{"case is not a way past it", "?domain=ABC.EXAMPLE.COM", http.StatusOK},
		{"a port does not stop the lookup", "?domain=abc.example.com:443", http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ask(t, resolver, tc.query); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// Identity unreachable must not become "yes". Refusing costs new certificates
// until it is back; agreeing spends the CA's rate limit on hostnames nobody
// has vouched for, which costs every legitimate domain after it.
func TestAskHandler_IdentityDownRefuses(t *testing.T) {
	// A lookup that resolves to nothing makes the resolver report ErrUnavailable.
	resolver := auth.NewDomainResolver(noBaseURLLookup{}, "secret")

	if got := ask(t, resolver, "?domain=abc.example.com"); got == http.StatusOK {
		t.Error("status = 200 with Identity unreachable — a certificate must not be issued on no evidence")
	}
}

type noBaseURLLookup struct{}

func (noBaseURLLookup) GetBaseURL(string) (string, bool) { return "", false }

// Configuration mistakes should be visible as a listener that does not exist,
// rather than one that answers "no" to everything and reads like a DNS or CA
// fault.
func TestNewOnDemandTLS_OffUnlessConfigured(t *testing.T) {
	resolver := identityStub(t, "abc.example.com")

	if srv := NewOnDemandTLS(resolver, ""); srv != nil {
		t.Error("no address configured, but a server was built")
	}
	if srv := NewOnDemandTLS(nil, "127.0.0.1:0"); srv != nil {
		t.Error("no resolver (PLUGIN_API_KEY unset), but a server was built")
	}
	if srv := NewOnDemandTLS(resolver, "127.0.0.1:0"); srv == nil {
		t.Fatal("configured and able to ask Identity, but no server was built")
	}
}
