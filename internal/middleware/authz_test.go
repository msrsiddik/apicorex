package middleware

import "testing"

func TestPermissionAllowed(t *testing.T) {
	cases := []struct {
		granted  []string
		required string
		want     bool
	}{
		{[]string{"user:read"}, "user:read", true},
		{[]string{"user:read"}, "user:write", false},
		{[]string{"user:*"}, "user:write", true},
		{[]string{"*:*"}, "branch:manage", true},
		{[]string{"*:read"}, "branch:read", true},
		{[]string{"*:read"}, "branch:write", false},
		{[]string{"branch:read", "user:invite"}, "user:invite", true},
		{nil, "user:read", false},
		{[]string{"user:read"}, "", true}, // route declares no permission
	}
	for _, c := range cases {
		if got := PermissionAllowed(c.granted, c.required); got != c.want {
			t.Errorf("PermissionAllowed(%v, %q) = %v, want %v", c.granted, c.required, got, c.want)
		}
	}
}
