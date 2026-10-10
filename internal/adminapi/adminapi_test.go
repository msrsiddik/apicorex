package adminapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/msrsiddik/apicorex/internal/manifest"
	"github.com/msrsiddik/apicorex/internal/registry"
	"github.com/msrsiddik/apicorex/internal/snapshots"
	"github.com/msrsiddik/apicorex/internal/store"
)

type fakeProber struct {
	got []string
	err error
}

func (f *fakeProber) Probe(_ context.Context, dsn string) (ProbeResult, error) {
	f.got = append(f.got, dsn)
	if f.err != nil {
		return ProbeResult{}, f.err
	}
	return ProbeResult{CurrentUser: "app", MaxConnections: 100, ReservedConnections: 3}, nil
}

type fakeRegistry struct {
	names []string
	state map[string][]any
	// decl and settingsEnv stand in for a manifest's settings and what the
	// plugin reported about its environment.
	decl        map[string][]manifest.Setting
	settingsEnv map[string][]string
}

func (f fakeRegistry) AuthByName(name string) (string, bool) {
	st, ok := f.state[name]
	if !ok {
		return "", false
	}
	if len(st) > 2 {
		return st[2].(string), true
	}
	return "shared", true
}

func (f fakeRegistry) SettingsStateByName(name string) (registry.SettingsState, bool) {
	if _, ok := f.state[name]; !ok {
		return registry.SettingsState{}, false
	}
	return registry.SettingsState{Loaded: true, FromEnv: f.settingsEnv[name], Declared: f.decl[name]}, true
}

func (f fakeRegistry) Names() []string { return f.names }

func (f fakeRegistry) DBStateByName(name string) (string, int64, bool) {
	st, ok := f.state[name]
	if !ok {
		return "", 0, false
	}
	return st[0].(string), st[1].(int64), true
}

type harness struct {
	engine *gin.Engine
	store  *store.Store
	prober *fakeProber
}

func newHarness(t *testing.T, writable, withKey bool, registered ...string) *harness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var key []byte
	if withKey {
		key = make([]byte, 32)
		rand.Read(key)
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "core.db"), key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p := &fakeProber{}
	e := gin.New()
	reg := fakeRegistry{names: registered, state: map[string][]any{}, decl: testDecl, settingsEnv: map[string][]string{"schoolyze": {"PUBLIC_BASE_URL"}}}
	for _, n := range registered {
		reg.state[n] = []any{"core", int64(1)}
	}
	New(st, reg, writable, p).Mount(e.Group("/_core/admin"))
	return &harness{engine: e, store: st, prober: p}
}

func (h *harness) do(t *testing.T, method, path string, body any) (int, map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.engine.ServeHTTP(w, req)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

func TestWritesRefusedWithoutLogin(t *testing.T) {
	h := newHarness(t, false, true)
	for _, r := range []struct{ method, path string }{
		{http.MethodPut, "/_core/admin/db-config/schoolyze"},
		{http.MethodDelete, "/_core/admin/db-config/schoolyze"},
		{http.MethodPost, "/_core/admin/db-config/schoolyze/rollback"},
		{http.MethodPost, "/_core/admin/db-config/schoolyze/test"},
		{http.MethodPost, "/_core/admin/commands/schoolyze"},
	} {
		if code, _, _ := h.do(t, r.method, r.path, map[string]any{}); code != http.StatusForbidden {
			t.Errorf("%s %s: %d, want 403", r.method, r.path, code)
		}
	}
	// Reads stay open: they show nothing secret.
	if code, _, _ := h.do(t, http.MethodGet, "/_core/admin/db-config", nil); code != http.StatusOK {
		t.Errorf("list: %d", code)
	}
}

func TestSaveThenListShowsRedactedDSNAndRegisteredPlugins(t *testing.T) {
	h := newHarness(t, true, true, "schoolyze", "accounting")
	code, _, body := h.do(t, http.MethodPut, "/_core/admin/db-config/*", map[string]any{
		"dsn_action": "set", "dsn": "postgres://app:hunter2@db:5432/apicorex",
		"pool": map[string]any{"max_open": 20},
	})
	if code != http.StatusOK {
		t.Fatalf("save default: %d %s", code, body)
	}
	if strings.Contains(body, "hunter2") {
		t.Fatalf("save response carried the password: %s", body)
	}
	h.do(t, http.MethodPut, "/_core/admin/db-config/schoolyze", map[string]any{"pool": map[string]any{"max_open": 25}})

	code, out, body := h.do(t, http.MethodGet, "/_core/admin/db-config", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, body)
	}
	if strings.Contains(body, "hunter2") {
		t.Fatalf("list carried the password: %s", body)
	}
	plugins := out["plugins"].([]any)
	if len(plugins) != 2 {
		t.Fatalf("want both registered plugins listed, got %v", plugins)
	}
	byName := map[string]map[string]any{}
	for _, p := range plugins {
		m := p.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if byName["accounting"]["dsn_source"] != "default" || byName["accounting"]["has_own_row"] != false {
		t.Errorf("accounting: %v", byName["accounting"])
	}
	if byName["schoolyze"]["running"] != "core" || byName["schoolyze"]["running_version"].(float64) != 1 {
		t.Errorf("running state not reported: %v", byName["schoolyze"])
	}
	if eff := byName["schoolyze"]["effective"].(map[string]any); eff["max_open"].(float64) != 25 {
		t.Errorf("schoolyze effective: %v", eff)
	}
	if out["total_max_open"].(float64) != 45 {
		t.Errorf("total_max_open %v, want 45", out["total_max_open"])
	}
}

func TestErrorStatuses(t *testing.T) {
	h := newHarness(t, true, false)
	if code, out, _ := h.do(t, http.MethodPut, "/_core/admin/db-config/schoolyze", map[string]any{"pool": map[string]any{"max_open": 0}}); code != http.StatusBadRequest {
		t.Errorf("invalid pool: %d %v", code, out)
	}
	if code, out, _ := h.do(t, http.MethodPut, "/_core/admin/db-config/schoolyze", map[string]any{"dsn_action": "set", "dsn": "postgres://u:p@h/db"}); code != http.StatusConflict || !strings.Contains(out["error"].(string), "CORE_MASTER_KEY") {
		t.Errorf("no master key: %d %v", code, out)
	}
	if code, _, _ := h.do(t, http.MethodDelete, "/_core/admin/db-config/nothing-here", nil); code != http.StatusNotFound {
		t.Errorf("delete missing: %d", code)
	}
}

func TestTestUsesTypedDSNOrStoredOne(t *testing.T) {
	h := newHarness(t, true, true)
	h.do(t, http.MethodPut, "/_core/admin/db-config/schoolyze", map[string]any{"dsn_action": "set", "dsn": "postgres://u:stored@h/db"})

	code, out, _ := h.do(t, http.MethodPost, "/_core/admin/db-config/schoolyze/test", map[string]any{"dsn_action": "set", "dsn": "postgres://u:typed@h/db"})
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("typed: %d %v", code, out)
	}
	code, out, _ = h.do(t, http.MethodPost, "/_core/admin/db-config/schoolyze/test", map[string]any{"dsn_action": "keep"})
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("stored: %d %v", code, out)
	}
	if len(h.prober.got) != 2 || h.prober.got[0] != "postgres://u:typed@h/db" || h.prober.got[1] != "postgres://u:stored@h/db" {
		t.Fatalf("probed %v", h.prober.got)
	}

	// A typed DSN is validated before anything dials it.
	if code, _, _ := h.do(t, http.MethodPost, "/_core/admin/db-config/schoolyze/test", map[string]any{"dsn_action": "set", "dsn": "mysql://x"}); code != http.StatusBadRequest {
		t.Errorf("invalid typed dsn: %d", code)
	}
	if len(h.prober.got) != 2 {
		t.Error("an invalid DSN was dialled")
	}
}

func TestFailedProbeIsOKFalse(t *testing.T) {
	h := newHarness(t, true, true)
	h.prober.err = errors.New("connection refused")
	code, out, _ := h.do(t, http.MethodPost, "/_core/admin/db-config/schoolyze/test", map[string]any{"dsn_action": "set", "dsn": "postgres://u:p@h/db"})
	if code != http.StatusOK || out["ok"] != false || out["error"] != "connection refused" {
		t.Fatalf("%d %v", code, out)
	}
}

func TestPostgresUsesTheDefault(t *testing.T) {
	h := newHarness(t, true, true)
	if code, _, _ := h.do(t, http.MethodGet, "/_core/admin/postgres", nil); code != http.StatusNotFound {
		t.Errorf("no default yet: %d, want 404", code)
	}
	h.do(t, http.MethodPut, "/_core/admin/db-config/*", map[string]any{"dsn_action": "set", "dsn": "postgres://u:d@h/db"})
	code, out, _ := h.do(t, http.MethodGet, "/_core/admin/postgres", nil)
	if code != http.StatusOK || out["ok"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if res := out["result"].(map[string]any); res["max_connections"].(float64) != 100 {
		t.Errorf("%v", res)
	}
}

func TestRollbackAndHistoryAndAudit(t *testing.T) {
	h := newHarness(t, true, true)
	_, first, _ := h.do(t, http.MethodPut, "/_core/admin/db-config/schoolyze", map[string]any{"pool": map[string]any{"max_open": 5}})
	h.do(t, http.MethodPut, "/_core/admin/db-config/schoolyze", map[string]any{"pool": map[string]any{"max_open": 50}})

	if code, _, body := h.do(t, http.MethodPost, "/_core/admin/db-config/schoolyze/rollback", map[string]any{"version": first["version"]}); code != http.StatusOK {
		t.Fatalf("rollback: %d %s", code, body)
	}
	cfg, err := h.store.GetDBConfig(context.Background(), "schoolyze")
	if err != nil || *cfg.Pool.MaxOpen != 5 {
		t.Fatalf("%+v %v", cfg, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/_core/admin/db-config/schoolyze/history", nil)
	w := httptest.NewRecorder()
	h.engine.ServeHTTP(w, req)
	var hist []map[string]any
	json.Unmarshal(w.Body.Bytes(), &hist)
	if len(hist) != 3 || hist[0]["action"] != "rollback" {
		t.Fatalf("history: %v", hist)
	}

	req = httptest.NewRequest(http.MethodGet, "/_core/admin/audit?limit=10", nil)
	w = httptest.NewRecorder()
	h.engine.ServeHTTP(w, req)
	var audit []map[string]any
	json.Unmarshal(w.Body.Bytes(), &audit)
	if len(audit) != 3 || !strings.HasPrefix(audit[0]["actor"].(string), "dashboard@") {
		t.Fatalf("audit: %v", audit)
	}
}

func TestQueueAndListCommands(t *testing.T) {
	h := newHarness(t, true, false, "schoolyze")
	code, out, body := h.do(t, http.MethodPost, "/_core/admin/commands/schoolyze", map[string]any{"kind": "restart"})
	if code != http.StatusOK || out["state"] != "pending" || !strings.HasPrefix(out["requested_by"].(string), "dashboard@") {
		t.Fatalf("queue: %d %s", code, body)
	}
	if code, _, _ := h.do(t, http.MethodPost, "/_core/admin/commands/schoolyze", map[string]any{"kind": "format-disk"}); code != http.StatusBadRequest {
		t.Errorf("unknown kind: %d", code)
	}
	req := httptest.NewRequest(http.MethodGet, "/_core/admin/commands/schoolyze", nil)
	w := httptest.NewRecorder()
	h.engine.ServeHTTP(w, req)
	var list []map[string]any
	json.Unmarshal(w.Body.Bytes(), &list)
	if len(list) != 1 || list[0]["kind"] != "restart" {
		t.Fatalf("list: %s", w.Body.String())
	}
}

var testDecl = map[string][]manifest.Setting{
	"schoolyze": {
		{Key: "PDF_MAX_CONCURRENT", Type: manifest.SettingInt, Default: "3"},
		{Key: "PUBLIC_BASE_URL", Type: manifest.SettingURL},
		{Key: "PAYMENT_CRED_KEY", Type: manifest.SettingString, Secret: true, SetOnce: true},
		{Key: "PUSH_VAPID_PRIVATE", Type: manifest.SettingString, Secret: true},
	},
}

func TestSettingsSaveValidatesAgainstTheDeclaration(t *testing.T) {
	h := newHarness(t, true, false, "schoolyze")
	put := func(values map[string]any) (int, map[string]any) {
		code, out, _ := h.do(t, http.MethodPut, "/_core/admin/settings/schoolyze", map[string]any{"values": values})
		return code, out
	}
	if code, out := put(map[string]any{"PDF_MAX_CONCURRENT": "lots"}); code != http.StatusBadRequest {
		t.Errorf("bad int: %d %v", code, out)
	}
	if code, _ := put(map[string]any{"NOT_DECLARED": "x"}); code != http.StatusBadRequest {
		t.Errorf("undeclared key: %d", code)
	}
	// No master key in this harness: a secret cannot be sealed, so it is
	// refused rather than stored in the clear.
	if code, out := put(map[string]any{"PAYMENT_CRED_KEY": "k"}); code != http.StatusConflict || !strings.Contains(out["error"].(string), "CORE_MASTER_KEY") {
		t.Errorf("secret without a master key: %d %v", code, out)
	}
	if code, out := put(map[string]any{"PDF_MAX_CONCURRENT": "5", "PUBLIC_BASE_URL": "https://pay.example.com"}); code != http.StatusOK {
		t.Fatalf("valid save: %d %v", code, out)
	}
	// "" clears, like null.
	if code, _ := put(map[string]any{"PDF_MAX_CONCURRENT": ""}); code != http.StatusOK {
		t.Fatalf("clear: %d", code)
	}
	got, _ := h.store.ListSettings(context.Background(), "schoolyze")
	if _, still := got["PDF_MAX_CONCURRENT"]; still || got["PUBLIC_BASE_URL"].Value != "https://pay.example.com" {
		t.Fatalf("%+v", got)
	}
}

func TestSettingsNeedARegisteredPlugin(t *testing.T) {
	// Without the manifest there is nothing to validate against.
	h := newHarness(t, true, false)
	code, _, _ := h.do(t, http.MethodPut, "/_core/admin/settings/schoolyze", map[string]any{"values": map[string]any{"X": "1"}})
	if code != http.StatusConflict {
		t.Fatalf("%d", code)
	}
}

func TestSettingsListShowsSourceOfEachValue(t *testing.T) {
	h := newHarness(t, true, false, "schoolyze")
	h.do(t, http.MethodPut, "/_core/admin/settings/schoolyze", map[string]any{"values": map[string]any{"PDF_MAX_CONCURRENT": "5"}})
	code, _, body := h.do(t, http.MethodGet, "/_core/admin/settings", nil)
	if code != http.StatusOK {
		t.Fatal(code)
	}
	var list []map[string]any
	json.Unmarshal([]byte(body), &list)
	if len(list) != 1 || list[0]["plugin"] != "schoolyze" {
		t.Fatalf("%s", body)
	}
	byKey := map[string]map[string]any{}
	for _, s := range list[0]["settings"].([]any) {
		m := s.(map[string]any)
		byKey[m["key"].(string)] = m
	}
	if byKey["PDF_MAX_CONCURRENT"]["set"] != true || byKey["PDF_MAX_CONCURRENT"]["value"] != "5" {
		t.Errorf("set value: %v", byKey["PDF_MAX_CONCURRENT"])
	}
	if byKey["PUBLIC_BASE_URL"]["from_env"] != true || byKey["PUBLIC_BASE_URL"]["set"] != false {
		t.Errorf("env override: %v", byKey["PUBLIC_BASE_URL"])
	}
	if list[0]["version"].(float64) == 0 {
		t.Error("version not reported")
	}
}

func TestSettingsWritesNeedLogin(t *testing.T) {
	h := newHarness(t, false, false, "schoolyze")
	if code, _, _ := h.do(t, http.MethodPut, "/_core/admin/settings/schoolyze", map[string]any{"values": map[string]any{}}); code != http.StatusForbidden {
		t.Fatalf("%d", code)
	}
}

func TestKeysIssueListRevoke(t *testing.T) {
	h := newHarness(t, true, false, "schoolyze")
	code, out, body := h.do(t, http.MethodPost, "/_core/admin/keys/schoolyze", nil)
	if code != http.StatusOK || !strings.HasPrefix(out["key"].(string), "akx_") {
		t.Fatalf("issue: %d %s", code, body)
	}
	raw := out["key"].(string)
	id := int64(out["issued"].(map[string]any)["id"].(float64))

	code, _, body = h.do(t, http.MethodGet, "/_core/admin/keys", nil)
	if code != http.StatusOK || strings.Contains(body, raw) {
		t.Fatalf("the listing carried the key itself: %s", body)
	}
	var list struct {
		AcceptShared bool `json:"accept_shared"`
		Plugins      []struct {
			Plugin string `json:"plugin"`
			Auth   string `json:"auth"`
			Keys   []any  `json:"keys"`
		} `json:"plugins"`
	}
	json.Unmarshal([]byte(body), &list)
	if !list.AcceptShared || len(list.Plugins) != 1 || len(list.Plugins[0].Keys) != 1 || list.Plugins[0].Auth != "shared" {
		t.Fatalf("%s", body)
	}

	if code, _, _ := h.do(t, http.MethodDelete, fmt.Sprintf("/_core/admin/keys/schoolyze/%d", id), nil); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	if _, ok, _ := h.store.LookupPluginKey(context.Background(), raw); ok {
		t.Fatal("revoked through the API but still works")
	}
}

func TestSharedKeyOffIsRefusedWhileAPluginUsesIt(t *testing.T) {
	h := newHarness(t, true, false, "schoolyze") // registered on the shared key
	code, out, _ := h.do(t, http.MethodPut, "/_core/admin/shared-key", map[string]any{"accept": false})
	if code != http.StatusConflict || !strings.Contains(fmt.Sprint(out["still_on_shared"]), "schoolyze") {
		t.Fatalf("%d %v", code, out)
	}
	if code, _, _ := h.do(t, http.MethodPut, "/_core/admin/shared-key", map[string]any{"accept": false, "force": true}); code != http.StatusOK {
		t.Fatalf("forced: %d", code)
	}
	if on, _ := h.store.AcceptSharedKey(context.Background()); on {
		t.Fatal("still accepted")
	}
}

func TestKeyWritesNeedLogin(t *testing.T) {
	h := newHarness(t, false, false, "schoolyze")
	if code, _, _ := h.do(t, http.MethodPost, "/_core/admin/keys/schoolyze", nil); code != http.StatusForbidden {
		t.Fatalf("issue without login: %d", code)
	}
	if code, _, _ := h.do(t, http.MethodPut, "/_core/admin/shared-key", map[string]any{"accept": false, "force": true}); code != http.StatusForbidden {
		t.Fatalf("shared-key without login: %d", code)
	}
}

func TestSecretSettingsThroughTheAPI(t *testing.T) {
	h := newHarness(t, true, true, "schoolyze")
	put := func(body map[string]any) (int, map[string]any, string) {
		return h.do(t, http.MethodPut, "/_core/admin/settings/schoolyze", body)
	}
	if code, out, _ := put(map[string]any{"values": map[string]any{"PAYMENT_CRED_KEY": "first-key"}}); code != http.StatusOK {
		t.Fatalf("first set of a set-once secret needs no confirmation: %d %v", code, out)
	}

	// The listing never carries it.
	_, _, body := h.do(t, http.MethodGet, "/_core/admin/settings", nil)
	if strings.Contains(body, "first-key") {
		t.Fatalf("secret in the listing: %s", body)
	}

	// Replacing it needs its name confirmed.
	code, out, _ := put(map[string]any{"values": map[string]any{"PAYMENT_CRED_KEY": "second-key"}})
	if code != http.StatusConflict || out["set_once"] != "PAYMENT_CRED_KEY" {
		t.Fatalf("replaced a set-once secret unconfirmed: %d %v", code, out)
	}
	if code, out, _ := put(map[string]any{"values": map[string]any{"PAYMENT_CRED_KEY": nil}}); code != http.StatusConflict {
		t.Fatalf("cleared a set-once secret unconfirmed: %d %v", code, out)
	}
	if code, out, _ := put(map[string]any{"values": map[string]any{"PAYMENT_CRED_KEY": "second-key"}, "confirm": []string{"PAYMENT_CRED_KEY"}}); code != http.StatusOK {
		t.Fatalf("confirmed replace: %d %v", code, out)
	}
	// An ordinary secret replaces without ceremony.
	put(map[string]any{"values": map[string]any{"PUSH_VAPID_PRIVATE": "p1"}})
	if code, out, _ := put(map[string]any{"values": map[string]any{"PUSH_VAPID_PRIVATE": "p2"}}); code != http.StatusOK {
		t.Fatalf("ordinary secret: %d %v", code, out)
	}

	// History shows the changes, never the values, and restores the first.
	_, _, hbody := h.do(t, http.MethodGet, "/_core/admin/settings/schoolyze/history", nil)
	if strings.Contains(hbody, "first-key") || strings.Contains(hbody, "second-key") {
		t.Fatalf("secret in history: %s", hbody)
	}
	var hist []map[string]any
	json.Unmarshal([]byte(hbody), &hist)
	var first float64
	for _, c := range hist {
		if c["key"] == "PAYMENT_CRED_KEY" {
			first = c["version"].(float64) // oldest last; keep overwriting
		}
	}
	if code, out, _ := h.do(t, http.MethodPost, "/_core/admin/settings/schoolyze/restore", map[string]any{"version": first}); code != http.StatusOK {
		t.Fatalf("restore: %d %v", code, out)
	}
	got, _, _ := h.store.SettingsForPlugin(context.Background(), "schoolyze", true)
	if got["PAYMENT_CRED_KEY"] != "first-key" {
		t.Fatalf("restored %q", got["PAYMENT_CRED_KEY"])
	}
}

func TestSettingsOfAPluginThatIsDown(t *testing.T) {
	// A plugin that will not start without a setting, or crash-loops on a bad
	// one, is not registered — and is exactly when the setting needs fixing.
	h := newHarness(t, true, true) // nothing registered
	decl, _ := json.Marshal(testDecl["schoolyze"])
	if err := h.store.SaveDeclarations(context.Background(), "schoolyze", decl); err != nil {
		t.Fatal(err)
	}
	code, out, _ := h.do(t, http.MethodPut, "/_core/admin/settings/schoolyze", map[string]any{"values": map[string]any{"PDF_MAX_CONCURRENT": "4", "PAYMENT_CRED_KEY": "k"}})
	if code != http.StatusOK {
		t.Fatalf("save for a down plugin: %d %v", code, out)
	}
	if code, out, _ := h.do(t, http.MethodPut, "/_core/admin/settings/schoolyze", map[string]any{"values": map[string]any{"PDF_MAX_CONCURRENT": "many"}}); code != http.StatusBadRequest {
		t.Fatalf("still validated against the recorded declaration: %d %v", code, out)
	}
	_, _, body := h.do(t, http.MethodGet, "/_core/admin/settings", nil)
	if !strings.Contains(body, `"plugin":"schoolyze"`) || !strings.Contains(body, `"registered":false`) {
		t.Fatalf("a down plugin missing from the listing: %s", body)
	}
}

func snapHarness(t *testing.T, writable bool) *harness {
	t.Helper()
	h := newHarness(t, writable, true)
	m, err := snapshots.New(snapshots.Config{Dir: t.TempDir(), Keep: 7}, h.store, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Re-mount with snapshots on a fresh engine.
	h.engine = gin.New()
	a := New(h.store, fakeRegistry{state: map[string][]any{}}, writable, h.prober)
	a.SetSnapshots(m)
	a.Mount(h.engine.Group("/_core/admin"))
	return h
}

func TestStoreSnapshotsThroughTheAPI(t *testing.T) {
	h := snapHarness(t, true)
	code, out, body := h.do(t, http.MethodPost, "/_core/admin/store/snapshots", nil)
	if code != http.StatusOK || !strings.HasPrefix(out["name"].(string), "core-") {
		t.Fatalf("take: %d %s", code, body)
	}
	name := out["name"].(string)

	code, _, body = h.do(t, http.MethodGet, "/_core/admin/store/snapshots", nil)
	if code != http.StatusOK || !strings.Contains(body, name) || !strings.Contains(body, `"remote_configured":false`) {
		t.Fatalf("list: %d %s", code, body)
	}

	req := httptest.NewRequest(http.MethodGet, "/_core/admin/store/snapshots/"+name, nil)
	w := httptest.NewRecorder()
	h.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "SQLite format 3") {
		t.Fatalf("download: %d %q", w.Code, w.Body.String()[:min(20, w.Body.Len())])
	}
	if code, _, _ := h.do(t, http.MethodGet, "/_core/admin/store/snapshots/..%2Fcore.db", nil); code != http.StatusNotFound {
		t.Errorf("path outside the snapshots: %d", code)
	}

	audit, _ := h.store.ListAudit(context.Background(), 5, 0)
	var actions []string
	for _, a := range audit {
		actions = append(actions, a.Action)
	}
	if !strings.Contains(strings.Join(actions, ","), "store.snapshot.download") || !strings.Contains(strings.Join(actions, ","), "store.snapshot") {
		t.Fatalf("not audited: %v", actions)
	}
}

func TestStoreSnapshotsNeedLogin(t *testing.T) {
	h := snapHarness(t, false)
	if code, _, _ := h.do(t, http.MethodPost, "/_core/admin/store/snapshots", nil); code != http.StatusForbidden {
		t.Errorf("take without login: %d", code)
	}
	if code, _, _ := h.do(t, http.MethodGet, "/_core/admin/store/snapshots/core-20261010T023000Z.db", nil); code != http.StatusForbidden {
		t.Errorf("download without login: %d", code)
	}
}

func TestStoreSnapshotsOff(t *testing.T) {
	h := newHarness(t, true, false)
	if code, _, _ := h.do(t, http.MethodGet, "/_core/admin/store/snapshots", nil); code != http.StatusServiceUnavailable {
		t.Errorf("%d", code)
	}
}
