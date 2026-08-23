package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// An unauthenticated request used to get 401 JSON whatever asked for it, which
// is right for an API client and wrong for a person: a browser landed on
// {"error":"missing authorization header"} where a login form belongs. These
// pin who gets which answer.

func TestWantsHTMLOnlyForBrowserGets(t *testing.T) {
	cases := []struct {
		name, method, accept string
		want                 bool
	}{
		{"a browser navigating", http.MethodGet, "text/html,application/xhtml+xml", true},
		{"HEAD from a browser", http.MethodHead, "text/html", true},
		{"an API client", http.MethodGet, "application/json", false},
		{"no Accept at all", http.MethodGet, "", false},
		// Redirecting a POST would drop whatever the user submitted and land
		// them on a login with the body gone.
		{"a form submission", http.MethodPost, "text/html", false},
		{"an API write", http.MethodPost, "application/json", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(c.method, "/accounting/accounts", nil)
			if c.accept != "" {
				r.Header.Set("Accept", c.accept)
			}
			if got := wantsHTML(r); got != c.want {
				t.Fatalf("wantsHTML = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRejectRedirectsBrowsersAndKeepsWhereTheyWereGoing(t *testing.T) {
	w := httptest.NewRecorder()
	c := ginContextFor(w, httptest.NewRequest(http.MethodGet, "/accounting/accounts?as_of=2026-08-01", nil))
	c.Request.Header.Set("Accept", "text/html")

	rejectUnauthenticated(c, "/school/login", "missing authorization header")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303", w.Code)
	}
	// The query has to survive, or signing in drops the user somewhere other
	// than the page they asked for.
	if got, want := w.Header().Get("Location"),
		"/school/login?next=%2Faccounting%2Faccounts%3Fas_of%3D2026-08-01"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

func TestRejectKeepsJSONForApiClients(t *testing.T) {
	w := httptest.NewRecorder()
	c := ginContextFor(w, httptest.NewRequest(http.MethodGet, "/students", nil))
	c.Request.Header.Set("Accept", "application/json")

	rejectUnauthenticated(c, "/school/login", "missing authorization header")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401 — an API client cannot follow a login", w.Code)
	}
}

func TestRejectFallsBackToJSONWithoutALoginURL(t *testing.T) {
	// The default. A deployment that configures nothing behaves exactly as it
	// did before this existed.
	w := httptest.NewRecorder()
	c := ginContextFor(w, httptest.NewRequest(http.MethodGet, "/accounting/accounts", nil))
	c.Request.Header.Set("Accept", "text/html")

	rejectUnauthenticated(c, "", "missing authorization header")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", w.Code)
	}
}

// ginContextFor builds the minimum gin needs to record a response.
func ginContextFor(w *httptest.ResponseRecorder, r *http.Request) *gin.Context {
	gin.SetMode(gin.ReleaseMode)
	c, _ := gin.CreateTestContext(w)
	c.Request = r
	return c
}
