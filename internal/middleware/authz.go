package middleware

import "strings"

// PermissionAllowed reports whether the caller's granted permission set satisfies
// the required permission, honoring "*" wildcards in either segment ("branch:*",
// "*:*"). An empty required permission is always allowed (route declares none).
func PermissionAllowed(granted []string, required string) bool {
	if required == "" {
		return true
	}
	for _, g := range granted {
		if permMatch(g, required) {
			return true
		}
	}
	return false
}

func permMatch(granted, required string) bool {
	if granted == required || granted == "*:*" {
		return true
	}
	gr, ga, ok1 := strings.Cut(granted, ":")
	rr, ra, ok2 := strings.Cut(required, ":")
	if !ok1 || !ok2 {
		return false
	}
	return (gr == "*" || gr == rr) && (ga == "*" || ga == ra)
}
