package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CORS returns a middleware that handles cross-origin requests from browser
// clients (e.g. the Compose Web / WASM app). It must run before the auth
// middleware so the browser's OPTIONS preflight gets CORS headers instead of a
// 401 — otherwise the browser blocks the real request before it is ever sent.
//
// allowedOrigins is a list of exact origins (e.g. "http://localhost:8081"). An
// empty list, or a list containing "*", allows any origin (echoed back so that
// credentialed requests still work). Configure via CORS_ALLOWED_ORIGINS.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowAll := len(allowedOrigins) == 0
	allow := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
		}
		if o != "" {
			allow[o] = true
		}
	}

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && (allowAll || allow[origin]) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			reqHeaders := c.GetHeader("Access-Control-Request-Headers")
			if reqHeaders == "" {
				reqHeaders = "Authorization, Content-Type"
			}
			c.Header("Access-Control-Allow-Headers", reqHeaders)
			c.Header("Access-Control-Max-Age", "600")
		}

		// short-circuit the preflight: no auth, no proxying.
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// ParseOrigins splits a comma-separated CORS_ALLOWED_ORIGINS value.
func ParseOrigins(env string) []string {
	if strings.TrimSpace(env) == "" {
		return nil
	}
	parts := strings.Split(env, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}
