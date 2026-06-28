package protection

import "testing"

func TestIsRouteBlocked(t *testing.T) {
	cases := []struct {
		path    string
		blocked bool
	}{
		{"/_core/register", true},
		{"/admin/users", true},
		{"/internal/debug", true},
		{"/auth/login", false},
		{"/hello", false},
		{"/invoices/123", false},
	}
	for _, c := range cases {
		if got := IsRouteBlocked(c.path); got != c.blocked {
			t.Errorf("IsRouteBlocked(%q) = %v, want %v", c.path, got, c.blocked)
		}
	}
}

func TestIsBodyTooLarge(t *testing.T) {
	if IsBodyTooLarge(5 * 1024 * 1024) {
		t.Error("5MB should be allowed")
	}
	if !IsBodyTooLarge(11 * 1024 * 1024) {
		t.Error("11MB should be rejected")
	}
}

func TestStatusClass(t *testing.T) {
	cases := map[int]string{
		200: "2xx", 204: "2xx",
		301: "3xx",
		404: "4xx", 429: "4xx",
		500: "5xx", 502: "5xx",
		100: "other",
	}
	for code, want := range cases {
		if got := StatusClass(code); got != want {
			t.Errorf("StatusClass(%d) = %q, want %q", code, got, want)
		}
	}
}
