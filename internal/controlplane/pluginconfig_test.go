package controlplane

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/openapi"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
	"github.com/msrsiddik/apicorex/internal/store"
)

type cpHarness struct {
	t      *testing.T
	engine *gin.Engine
	reg    *registry.Registry
	store  *store.Store
}

func newCPHarness(t *testing.T, allowlist []string, withStore bool) *cpHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	reg := registry.New()
	disp := dispatcher.New(reg, protection.NewCircuitBreaker(5, 0), protection.NewBulkhead(10), config.Defaults())
	h := New(reg, disp, openapi.NewInjector(), "plugin-key", allowlist, "plugin-key", "dashboard-secret")
	out := &cpHarness{t: t, reg: reg}
	if withStore {
		key := make([]byte, 32)
		rand.Read(key)
		st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "core.db"), key)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		h.SetStore(st)
		out.store = st
	}
	e := gin.New()
	h.Mount(e)
	out.engine = e
	return out
}

func (h *cpHarness) post(path string, body any) (int, map[string]any) {
	h.t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.engine.ServeHTTP(w, req)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// register stands up a fake plugin serving a manifest and registers it.
func (h *cpHarness) register(name string) (id, token string) {
	h.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"name":%q,"version":"1.0.0","routes":[]}`, name)
	}))
	h.t.Cleanup(srv.Close)
	code, out := h.post("/_core/register", map[string]string{"base_url": srv.URL, "api_key": "plugin-key"})
	if code != http.StatusOK {
		h.t.Fatalf("register: %d %v", code, out)
	}
	return out["plugin_id"].(string), out["plugin_token"].(string)
}

func TestDBConfigFetch(t *testing.T) {
	h := newCPHarness(t, nil, true)
	ctx := context.Background()

	if code, _ := h.post("/_core/config/db", map[string]string{"api_key": "wrong", "plugin": "schoolyze"}); code != http.StatusUnauthorized {
		t.Errorf("wrong key: %d", code)
	}
	if code, out := h.post("/_core/config/db", map[string]string{"api_key": "plugin-key", "plugin": "schoolyze"}); code != http.StatusNotFound {
		t.Errorf("nothing configured: %d %v", code, out)
	}
	// The default row is not a plugin; nobody may ask for it by name.
	if code, _ := h.post("/_core/config/db", map[string]string{"api_key": "plugin-key", "plugin": "*"}); code != http.StatusBadRequest {
		t.Errorf("asked for the default row: %d", code)
	}

	max := 12
	if _, err := h.store.SaveDBConfig(ctx, store.DefaultPlugin, store.DBConfigInput{
		DSNAction: store.DSNSet, DSN: "postgres://app:pw@db:5432/apicorex", Pool: store.PoolSettings{MaxOpen: &max},
	}, "dashboard"); err != nil {
		t.Fatal(err)
	}
	code, out := h.post("/_core/config/db", map[string]string{"api_key": "plugin-key", "plugin": "schoolyze"})
	if code != http.StatusOK || out["dsn"] != "postgres://app:pw@db:5432/apicorex" || out["max_open"].(float64) != 12 || out["version"].(float64) < 1 {
		t.Fatalf("fetch: %d %v", code, out)
	}

	audit, _ := h.store.ListAudit(ctx, 1, 0)
	if len(audit) != 1 || audit[0].Action != "db_config.fetch" || audit[0].Target != "schoolyze" {
		t.Fatalf("fetch not audited: %+v", audit)
	}
}

func TestDBConfigFetchHonoursAllowlist(t *testing.T) {
	h := newCPHarness(t, []string{"schoolyze"}, true)
	if code, _ := h.post("/_core/config/db", map[string]string{"api_key": "plugin-key", "plugin": "intruder"}); code != http.StatusForbidden {
		t.Fatalf("unlisted plugin: %d", code)
	}
}

func TestHeartbeatCarriesVersionAndCommand(t *testing.T) {
	h := newCPHarness(t, nil, true)
	ctx := context.Background()
	id, token := h.register("schoolyze")

	saved, err := h.store.SaveDBConfig(ctx, store.DefaultPlugin, store.DBConfigInput{DSNAction: store.DSNSet, DSN: "postgres://a:a@h/db"}, "dashboard")
	if err != nil {
		t.Fatal(err)
	}

	beat := map[string]any{"plugin_id": id, "plugin_token": token, "db_source": "core", "db_version": saved.Version}
	code, out := h.post("/_core/heartbeat", beat)
	if code != http.StatusOK || out["config_version"].(float64) != float64(saved.Version) {
		t.Fatalf("heartbeat: %d %v", code, out)
	}
	if _, has := out["command"]; has {
		t.Fatalf("command with none queued: %v", out)
	}
	if src, ver, ok := h.reg.DBStateByName("schoolyze"); !ok || src != "core" || ver != saved.Version {
		t.Fatalf("reported state not kept: %q %d %v", src, ver, ok)
	}

	cmd, err := h.store.QueueCommand(ctx, "schoolyze", store.CommandReload, "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	_, out = h.post("/_core/heartbeat", beat)
	got, ok := out["command"].(map[string]any)
	if !ok || got["kind"] != "reload" || got["id"].(float64) != float64(cmd.ID) {
		t.Fatalf("command not delivered: %v", out)
	}
	if _, out = h.post("/_core/heartbeat", beat); out["command"] != nil {
		t.Fatalf("command delivered twice: %v", out)
	}

	path := fmt.Sprintf("/_core/commands/%d/result", cmd.ID)
	if code, _ := h.post(path, map[string]any{"plugin_id": id, "plugin_token": "forged", "ok": true}); code != http.StatusUnauthorized {
		t.Errorf("forged token: %d", code)
	}
	if code, out := h.post(path, map[string]any{"plugin_id": id, "plugin_token": token, "ok": true, "message": "pool swapped"}); code != http.StatusOK {
		t.Fatalf("result: %d %v", code, out)
	}
	list, _ := h.store.ListCommands(ctx, "schoolyze", 1)
	if list[0].State != "done" || list[0].Result != "pool swapped" {
		t.Fatalf("%+v", list[0])
	}
}

func TestCommandResultFromAnotherPlugin(t *testing.T) {
	h := newCPHarness(t, nil, true)
	ctx := context.Background()
	h.register("accounting")
	otherID, otherToken := h.register("schoolyze")
	cmd, _ := h.store.QueueCommand(ctx, "accounting", store.CommandRestart, "dashboard")
	h.store.TakeCommand(ctx, "accounting")

	code, _ := h.post(fmt.Sprintf("/_core/commands/%d/result", cmd.ID), map[string]any{"plugin_id": otherID, "plugin_token": otherToken, "ok": true})
	if code != http.StatusNotFound {
		t.Fatalf("schoolyze closed accounting's command: %d", code)
	}
}

func TestHeartbeatWithoutStoreIsUnchanged(t *testing.T) {
	// What every deployment had before the store: a plain acknowledgement.
	h := newCPHarness(t, nil, false)
	id, token := h.register("schoolyze")
	code, out := h.post("/_core/heartbeat", map[string]any{"plugin_id": id, "plugin_token": token})
	if code != http.StatusOK || out["acknowledged"] != true || out["config_version"] != nil || out["command"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := h.post("/_core/config/db", map[string]string{"api_key": "plugin-key", "plugin": "schoolyze"}); code != http.StatusServiceUnavailable {
		t.Errorf("config fetch without a store: %d", code)
	}
}

func TestRestartResultOpensWindowThatRegistrationCloses(t *testing.T) {
	h := newCPHarness(t, nil, true)
	ctx := context.Background()
	id, token := h.register("schoolyze")
	cmd, _ := h.store.QueueCommand(ctx, "schoolyze", store.CommandRestart, "dashboard")
	h.store.TakeCommand(ctx, "schoolyze")

	if code, out := h.post(fmt.Sprintf("/_core/commands/%d/result", cmd.ID), map[string]any{"plugin_id": id, "plugin_token": token, "ok": true, "message": "restarting"}); code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	if w, ok := h.reg.MaintenanceFor("schoolyze"); !ok || !w.EndsOnRegister {
		t.Fatalf("no restart window while the plugin is down: %+v", w)
	}

	h.register("schoolyze")
	if _, ok := h.reg.MaintenanceFor("schoolyze"); ok {
		t.Fatal("restart window still open after the plugin came back")
	}
}

func TestFailedReloadOpensNoWindow(t *testing.T) {
	h := newCPHarness(t, nil, true)
	ctx := context.Background()
	id, token := h.register("schoolyze")
	cmd, _ := h.store.QueueCommand(ctx, "schoolyze", store.CommandReload, "dashboard")
	h.store.TakeCommand(ctx, "schoolyze")
	h.post(fmt.Sprintf("/_core/commands/%d/result", cmd.ID), map[string]any{"plugin_id": id, "plugin_token": token, "ok": false, "message": "ping failed"})
	if _, ok := h.reg.MaintenanceFor("schoolyze"); ok {
		t.Fatal("a reload opened a maintenance window")
	}
}
