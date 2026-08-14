package middleware

import (
	"net/http"
	"testing"
)

// The features header. It rides the same introspection call as permissions, so
// what needs proving is not that it arrives but that it cannot be faked and is
// not confused with per-user authorization.

// The tenant's modules reach the plugin as a trusted header.
func TestFeatures_InjectedFromIdentity(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	w := doAuth(r, map[string]string{"Authorization": "Bearer zdt_good"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	if got := captured.Get(HeaderFeatures); got != "schoolyze:attendance,schoolyze:fees" {
		t.Errorf("features header = %q, want the tenant's modules", got)
	}
}

// The one that matters. A client that sets the header itself must not be able
// to hand itself a module: without stripping, anyone could unlock every paid
// feature by adding a request header, and no plugin would be able to tell.
func TestFeatures_ClientSuppliedHeaderIsStripped(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	w := doAuth(r, map[string]string{
		"Authorization": "Bearer zdt_good",
		HeaderFeatures:  "schoolyze:online_payment,schoolyze:payroll",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body)
	}
	got := captured.Get(HeaderFeatures)
	if got != "schoolyze:attendance,schoolyze:fees" {
		t.Errorf("features header = %q, want only what Identity resolved", got)
	}
	for _, forged := range []string{"online_payment", "payroll"} {
		if contains(got, forged) {
			t.Errorf("forged feature %q survived into the trusted header", forged)
		}
	}
}

// An unauthenticated request carries no features at all — a public route must
// not look like a tenant with every module enabled.
func TestFeatures_AbsentWhenUnauthenticated(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	// No bearer: Auth leaves the request unauthenticated and injection is a
	// no-op, so nothing the client sent may remain either.
	doAuth(r, map[string]string{HeaderFeatures: "schoolyze:fees"})
	if got := captured.Get(HeaderFeatures); got != "" {
		t.Errorf("features header = %q, want empty on an unauthenticated request", got)
	}
}

// Switching the acting user must not change the features: they belong to the
// institution, not the person. A member and an owner of the same tenant see the
// same modules and differ only in what they may do inside them.
func TestFeatures_SameForEveryUserOfATenant(t *testing.T) {
	srv := newIdentityStub(t)
	defer srv.Close()
	r, captured := authTestRig(t, srv.URL)

	doAuth(r, map[string]string{"Authorization": "Bearer zdt_good"})
	asOwner := captured.Get(HeaderFeatures)

	doAuth(r, map[string]string{
		"Authorization":  "Bearer zdt_good",
		HeaderActingUser: "u_staff",
	})
	asStaff := captured.Get(HeaderFeatures)

	if asOwner != asStaff {
		t.Errorf("features differ by user: owner %q, staff %q", asOwner, asStaff)
	}
	if captured.Get(HeaderPermissions) == "*:*" {
		t.Error("acting user's permissions did not narrow; the rig is not exercising the switch")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
