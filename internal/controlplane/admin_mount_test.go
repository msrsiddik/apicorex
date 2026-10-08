package controlplane

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/openapi"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
)

// Routes added through MountAdmin change where plugins connect, so they must
// sit behind the same session as the dashboard's own write actions — never
// beside them.
func TestMountAdminRoutesNeedASession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	reg := registry.New()
	disp := dispatcher.New(reg, protection.NewCircuitBreaker(5, 0), protection.NewBulkhead(10), config.Defaults())
	h := New(reg, disp, openapi.NewInjector(), "plugin-key", nil, "plugin-key", "dashboard-secret")
	h.MountAdmin(func(g *gin.RouterGroup) {
		g.GET("/probe", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	})
	e := gin.New()
	h.Mount(e)

	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/_core/admin/probe", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("without a session: %d, want 401", w.Code)
	}

	body, _ := json.Marshal(map[string]string{"key": "dashboard-secret"})
	w = httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/_core/admin/login", bytes.NewReader(body)))
	var login struct{ Token string }
	json.Unmarshal(w.Body.Bytes(), &login)
	if login.Token == "" {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/_core/admin/probe", nil)
	req.Header.Set("Authorization", "Bearer "+login.Token)
	w = httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("with a session: %d, want 204", w.Code)
	}

	if !h.LoginEnabled() {
		t.Error("LoginEnabled false with a secret set")
	}
}
