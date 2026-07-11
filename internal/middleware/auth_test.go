package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/auth"
)

// stubLookup satisfies the introspector's registry dependency with a fixed URL.
type stubLookup struct{ url string }

func (s stubLookup) GetBaseURL(string) (string, bool) { return s.url, s.url != "" }

// newIdentityStub runs a fake /internal/introspect: token hash of "zdt_good"
// resolves; acting user "u_staff" gets member perms; acting "u_gone" is revoked.
func newIdentityStub(t *testing.T) *httptest.Server {
	t.Helper()
	goodHash := auth.HashToken("zdt_good")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/introspect" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Plugin-Key") != "test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid plugin key"})
			return
		}
		var in struct {
			TokenHash    string `json:"token_hash"`
			ActingUserID string `json:"acting_user_id"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.TokenHash != goodHash {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid token"})
			return
		}
		switch in.ActingUserID {
		case "u_gone":
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "membership revoked"})
		case "u_staff":
			json.NewEncoder(w).Encode(auth.Identity{
				TenantID: "t_1", TenantSlug: "acme", SchemaName: "tenant_acme",
				UserID: "u_staff", UserType: "tenant_user",
				Roles: []string{"member"}, Permissions: []string{"user:read", "branch:read"},
			})
		default:
			json.NewEncoder(w).Encode(auth.Identity{
				TenantID: "t_1", TenantSlug: "acme", SchemaName: "tenant_acme",
				BranchID: "br_1", BranchSlug: "main",
				UserID: "u_owner", UserType: "tenant_user",
				Roles: []string{"owner"}, Permissions: []string{"*:*"},
			})
		}
	}))
}

// authTestRig wires StripSpoofedHeaders → Auth → InjectTenantHeaders → capture.
func authTestRig(t *testing.T, srvURL string) (*gin.Engine, *http.Header) {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	intro := auth.NewIntrospector(stubLookup{url: srvURL}, "test-key")
	captured := &http.Header{}
	r := gin.New()
	r.Use(StripSpoofedHeaders())
	r.Use(Auth(intro))
	r.GET("/x", func(c *gin.Context) {
		InjectTenantHeaders(c)
		*captured = c.Request.Header.Clone()
		c.Status(http.StatusOK)
	})
	return r, captured
}

func doAuth(r *gin.Engine, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/x", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A valid device token resolves to the OWNER by default; the trusted headers
// carry the owner's identity plus the token hash, and any spoofed X-ApiCoreX-*
// input has been stripped.
func TestAuth_OwnerDefault(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	w := doAuth(r, map[string]string{
		"Authorization":      "Bearer zdt_good",
		HeaderUserID:         "u_spoofed", // must be stripped, then overwritten
		HeaderPermissions:    "*:*",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := captured.Get(HeaderUserID); got != "u_owner" {
		t.Errorf("user id header = %q, want u_owner", got)
	}
	if got := captured.Get(HeaderTokenHash); got != auth.HashToken("zdt_good") {
		t.Errorf("token hash header = %q, want the bearer's hash", got)
	}
	if got := captured.Get(HeaderBranchSlug); got != "main" {
		t.Errorf("branch slug header = %q, want main", got)
	}
}

// X-Acting-User switches the injected identity to the acting user's fresh
// role/permissions, and the acting header itself is consumed (not forwarded).
func TestAuth_ActingUser(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	w := doAuth(r, map[string]string{
		"Authorization":  "Bearer zdt_good",
		HeaderActingUser: "u_staff",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := captured.Get(HeaderUserID); got != "u_staff" {
		t.Errorf("user id header = %q, want u_staff", got)
	}
	if got := captured.Get(HeaderPermissions); got != "user:read,branch:read" {
		t.Errorf("permissions header = %q, want member perms", got)
	}
	if got := captured.Get(HeaderActingUser); got != "" {
		t.Errorf("X-Acting-User must be consumed, still present: %q", got)
	}
}

// Error mapping: bad/missing bearer → 401 invalid token; removed acting user →
// 401 membership revoked (verbatim, so the app's revoke detection works);
// identity unreachable → 503, never a session-wiping 401.
func TestAuth_ErrorMapping(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, _ := authTestRig(t, srv.URL)

	if w := doAuth(r, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("no bearer = %d, want 401", w.Code)
	}
	if w := doAuth(r, map[string]string{"Authorization": "Bearer eyJhbGciOi.jwt.style"}); w.Code != http.StatusUnauthorized {
		t.Errorf("non-zdt bearer = %d, want 401", w.Code)
	}
	if w := doAuth(r, map[string]string{"Authorization": "Bearer zdt_wrong"}); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown token = %d, want 401", w.Code)
	}

	w := doAuth(r, map[string]string{"Authorization": "Bearer zdt_good", HeaderActingUser: "u_gone"})
	if w.Code != http.StatusUnauthorized {
		t.Errorf("revoked acting = %d, want 401", w.Code)
	}
	if body := w.Body.String(); !strings.Contains(body, "membership revoked") {
		t.Errorf("revoked acting body = %s, want membership revoked", body)
	}

	// identity down → 503
	rDown, _ := authTestRig(t, "http://127.0.0.1:1") // nothing listens here
	if w := doAuth(rDown, map[string]string{"Authorization": "Bearer zdt_good"}); w.Code != http.StatusServiceUnavailable {
		t.Errorf("identity down = %d, want 503", w.Code)
	}
}

// Introspection results are cached: a second resolve for the same (token,
// acting) pair within the TTL doesn't hit identity again.
func TestAuth_CachesIntrospection(t *testing.T) {
	calls := 0
	goodHash := auth.HashToken("zdt_good")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var in struct {
			TokenHash string `json:"token_hash"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.TokenHash != goodHash {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(auth.Identity{TenantID: "t_1", UserID: "u_owner", UserType: "tenant_user"})
	}))
	defer srv.Close()

	r, _ := authTestRig(t, srv.URL)
	for range 3 {
		if w := doAuth(r, map[string]string{"Authorization": "Bearer zdt_good"}); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	}
	if calls != 1 {
		t.Errorf("identity calls = %d, want 1 (cached)", calls)
	}
}

