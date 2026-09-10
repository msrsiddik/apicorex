package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// A client must not be able to name its own forwarded host.
//
// Worth its own test rather than trusting the name: apicorexHeaders is an
// explicit list, not a prefix match, so a new X-ApiCoreX-* header is spoofable
// until someone adds it there. Downstream this one decides which institution a
// hostname resolves to, which is not a decision a caller may make for itself.
func TestStripSpoofedHeaders_ForwardedHost(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var seen string
	r := gin.New()
	r.Use(StripSpoofedHeaders())
	r.GET("/x", func(c *gin.Context) {
		seen = c.GetHeader(HeaderForwardedHost)
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderForwardedHost, "victim.example.com")
	r.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "" {
		t.Errorf("%s = %q, want it stripped — a caller naming its own host picks its own tenant",
			HeaderForwardedHost, seen)
	}
}
