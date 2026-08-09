package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/auth"
)

// doAuthCookie issues a request carrying the device token as a cookie rather
// than a bearer header, plus any extra headers.
func doAuthCookie(r *gin.Engine, token string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/x", nil)
	if token != "" {
		req.AddCookie(&http.Cookie{Name: TokenCookie, Value: token})
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// A browser navigation cannot set an Authorization header, so a plugin serving
// HTML authenticates from a cookie. The resolved identity must be identical to
// the bearer path — same introspection, same injected headers.
func TestAuth_CookieAuthenticatesLikeBearer(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	w := doAuthCookie(r, "zdt_good", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := captured.Get(HeaderUserID); got != "u_owner" {
		t.Errorf("user id header = %q, want u_owner", got)
	}
	if got := captured.Get(HeaderTokenHash); got != auth.HashToken("zdt_good") {
		t.Errorf("token hash header = %q, want the cookie token's hash", got)
	}
	if got := captured.Get(HeaderTenantSlug); got != "acme" {
		t.Errorf("tenant slug header = %q, want acme", got)
	}
}

// An app sending both must not be affected by a stale cookie left on the same
// host: the header is the explicit choice and wins.
func TestAuth_HeaderWinsOverCookie(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer zdt_good")
	req.AddCookie(&http.Cookie{Name: TokenCookie, Value: "zdt_wrong"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the header token should have been used (%s)", w.Code, w.Body)
	}
	if got := captured.Get(HeaderTokenHash); got != auth.HashToken("zdt_good") {
		t.Errorf("token hash = %q, want the header token's hash", got)
	}
}

// A cookie is not a bypass: it goes through the same validation as a bearer.
func TestAuth_CookieRejectsBadTokens(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, _ := authTestRig(t, srv.URL)

	if w := doAuthCookie(r, "zdt_wrong", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("unknown cookie token = %d, want 401", w.Code)
	}
	// A non-device token must be rejected before introspection, exactly as on
	// the header path — the "zdt_" check is not header-specific.
	if w := doAuthCookie(r, "eyJhbGciOi.jwt.style", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("non-zdt cookie = %d, want 401", w.Code)
	}
	if w := doAuthCookie(r, "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("empty cookie = %d, want 401", w.Code)
	}
}

// An empty bearer must fall through to the cookie rather than failing outright:
// some clients send "Authorization: Bearer " with nothing after it.
func TestAuth_EmptyBearerFallsBackToCookie(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer ")
	req.AddCookie(&http.Cookie{Name: TokenCookie, Value: "zdt_good"})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := captured.Get(HeaderUserID); got != "u_owner" {
		t.Errorf("user id = %q, want u_owner", got)
	}
}

// The acting-user switch works the same on the cookie path — a shared
// staff-room browser must be able to hand over the same way a shared device does.
func TestAuth_CookieWithActingUser(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	w := doAuthCookie(r, "zdt_good", map[string]string{HeaderActingUser: "u_staff"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := captured.Get(HeaderUserID); got != "u_staff" {
		t.Errorf("user id = %q, want the acting user", got)
	}
	// The acting user's own permissions apply, not the token owner's.
	if got := captured.Get(HeaderPermissions); got == "*:*" {
		t.Error("the token owner's permissions leaked past the acting-user switch")
	}
	if captured.Get(HeaderActingUser) != "" {
		t.Error("the acting-user header should be consumed, not forwarded")
	}
}
