package adminapi

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/store"
)

// fakeProtection records refreshes and runs what it is told.
type fakeProtection struct {
	refreshed []string
	running   map[string]config.Limits
}

func (f *fakeProtection) RefreshLimits(name string) { f.refreshed = append(f.refreshed, name) }
func (f *fakeProtection) RefreshAllLimits()         { f.refreshed = append(f.refreshed, "all") }
func (f *fakeProtection) RunningLimits(name string) (config.Limits, bool) {
	l, ok := f.running[name]
	return l, ok
}

func newProtectionHarness(t *testing.T, writable bool, prot *fakeProtection) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "core.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	reg := fakeRegistry{names: []string{"billing"}, state: map[string][]any{"billing": {"core", int64(1)}}}
	h := New(st, reg, writable, &fakeProber{})
	if prot != nil {
		h.SetProtection(config.Defaults().Default, prot)
	}
	e := gin.New()
	h.Mount(e.Group("/_core/admin"))
	return &harness{engine: e, store: st}
}

func TestProtectionSaveAppliesAndLists(t *testing.T) {
	running := config.Defaults().Default
	prot := &fakeProtection{running: map[string]config.Limits{"billing": running}}
	h := newProtectionHarness(t, true, prot)

	code, out, body := h.do(t, http.MethodPut, "/_core/admin/protection/billing",
		map[string]any{"limits": map[string]any{"bulkhead_max": 12, "request_timeout_ms": 30000}, "note": "busy week"})
	if code != http.StatusOK {
		t.Fatalf("save: %d %s", code, body)
	}
	eff := out["effective"].(map[string]any)
	if eff["bulkhead_max"] != 12.0 || eff["request_timeout_ms"] != 30000.0 || eff["cb_threshold"] != 5.0 {
		t.Errorf("effective after save: %v", eff)
	}
	if src := out["source"].(map[string]any); src["bulkhead_max"] != "billing" || src["cb_threshold"] != "config" {
		t.Errorf("sources: %v", src)
	}
	if out["running"] == nil || !out["registered"].(bool) {
		t.Errorf("a registered plugin shows what it runs: %v", out)
	}

	// A change to the default refreshes every plugin.
	if code, _, body := h.do(t, http.MethodPut, "/_core/admin/protection/*", map[string]any{"limits": map[string]any{"cb_threshold": 3}}); code != http.StatusOK {
		t.Fatalf("save default: %d %s", code, body)
	}
	if len(prot.refreshed) != 2 || prot.refreshed[0] != "billing" || prot.refreshed[1] != "all" {
		t.Errorf("refreshed %v", prot.refreshed)
	}

	code, out, body = h.do(t, http.MethodGet, "/_core/admin/protection", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	if out["writable"] != true {
		t.Errorf("writable: %v", out["writable"])
	}
	plugins := out["plugins"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("plugins: %v", plugins)
	}
	billing := plugins[0].(map[string]any)["effective"].(map[string]any)
	if billing["cb_threshold"] != 3.0 || billing["bulkhead_max"] != 12.0 {
		t.Errorf("billing inherits the default's threshold and keeps its own bulkhead: %v", billing)
	}
	if out["default"].(map[string]any)["source"].(map[string]any)["cb_threshold"] != store.DefaultPlugin {
		t.Errorf("default view: %v", out["default"])
	}
	if out["config"].(map[string]any)["request_timeout_ms"] != float64((120 * time.Second).Milliseconds()) {
		t.Errorf("config layer: %v", out["config"])
	}
}

func TestProtectionRollbackAndDelete(t *testing.T) {
	prot := &fakeProtection{}
	h := newProtectionHarness(t, true, prot)
	h.do(t, http.MethodPut, "/_core/admin/protection/billing", map[string]any{"limits": map[string]any{"bulkhead_max": 12}})
	h.do(t, http.MethodPut, "/_core/admin/protection/billing", map[string]any{"limits": map[string]any{"bulkhead_max": 20}})

	hist, err := h.store.ProtectionHistory(context.Background(), "billing", 0)
	if err != nil || len(hist) != 2 {
		t.Fatalf("history %v %v", hist, err)
	}
	code, out, body := h.do(t, http.MethodPost, "/_core/admin/protection/billing/rollback", map[string]any{"version": hist[1].Version})
	if code != http.StatusOK || out["effective"].(map[string]any)["bulkhead_max"] != 12.0 {
		t.Fatalf("rollback: %d %s", code, body)
	}
	code, out, body = h.do(t, http.MethodDelete, "/_core/admin/protection/billing", nil)
	if code != http.StatusOK || out["effective"].(map[string]any)["bulkhead_max"] != 100.0 {
		t.Fatalf("delete: %d %s", code, body)
	}
	if code, _, _ := h.do(t, http.MethodDelete, "/_core/admin/protection/billing", nil); code != http.StatusNotFound {
		t.Errorf("second delete: %d", code)
	}
	if code, _, _ := h.do(t, http.MethodGet, "/_core/admin/protection/billing/history", nil); code != http.StatusOK {
		t.Errorf("history: %d", code)
	}
	if len(prot.refreshed) != 4 {
		t.Errorf("every write refreshes: %v", prot.refreshed)
	}
}

func TestProtectionErrors(t *testing.T) {
	h := newProtectionHarness(t, true, &fakeProtection{})
	if code, _, body := h.do(t, http.MethodPut, "/_core/admin/protection/billing", map[string]any{"limits": map[string]any{"bulkhead_max": 0}}); code != http.StatusBadRequest {
		t.Errorf("invalid value: %d %s", code, body)
	}
	if code, _, _ := h.do(t, http.MethodPost, "/_core/admin/protection/billing/rollback", map[string]any{}); code != http.StatusBadRequest {
		t.Errorf("rollback without a version: %d", code)
	}

	ro := newProtectionHarness(t, false, &fakeProtection{})
	if code, _, _ := ro.do(t, http.MethodPut, "/_core/admin/protection/billing", map[string]any{"limits": map[string]any{}}); code != http.StatusForbidden {
		t.Errorf("write without login: %d, want 403", code)
	}

	off := newProtectionHarness(t, true, nil)
	if code, _, _ := off.do(t, http.MethodGet, "/_core/admin/protection", nil); code != http.StatusServiceUnavailable {
		t.Errorf("not connected: %d, want 503", code)
	}
}
