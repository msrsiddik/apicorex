package controlplane

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/msrsiddik/apicorex/internal/store"
)

// registerWith registers a fake plugin named name, presenting key.
func (h *cpHarness) registerWith(name, key string) (int, map[string]any) {
	h.t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"name":%q,"version":"1.0.0","routes":[]}`, name)
	}))
	h.t.Cleanup(srv.Close)
	return h.post("/_core/register", map[string]string{"base_url": srv.URL, "api_key": key})
}

func (h *cpHarness) issue(plugin string) string {
	h.t.Helper()
	raw, _, err := h.store.IssuePluginKey(context.Background(), plugin, "dashboard")
	if err != nil {
		h.t.Fatal(err)
	}
	return raw
}

func TestRegisterWithOwnKey(t *testing.T) {
	h := newCPHarness(t, nil, true)
	key := h.issue("schoolyze")

	if code, out := h.registerWith("schoolyze", key); code != http.StatusOK {
		t.Fatalf("own key: %d %v", code, out)
	}
	if auth, _ := h.reg.AuthByName("schoolyze"); auth != "own" {
		t.Errorf("auth recorded as %q", auth)
	}

	// The hole this closes: registering as identity, which Core sends every
	// bearer token to for introspection.
	if code, _ := h.registerWith("identity", key); code != http.StatusForbidden {
		t.Fatalf("schoolyze's key registered identity: %d", code)
	}

	h.registerWith("accounting", "plugin-key")
	if auth, _ := h.reg.AuthByName("accounting"); auth != "shared" {
		t.Errorf("shared-key registration recorded as %q", auth)
	}
}

func TestConfigWithOwnKeyIsThatPlugins(t *testing.T) {
	h := newCPHarness(t, nil, true)
	ctx := context.Background()
	key := h.issue("schoolyze")
	h.store.SaveDBConfig(ctx, "schoolyze", store.DBConfigInput{DSNAction: store.DSNSet, DSN: "postgres://school:pw@h/db"}, "dashboard")
	h.store.SaveDBConfig(ctx, "accounting", store.DBConfigInput{DSNAction: store.DSNSet, DSN: "postgres://ledger:pw@h/db"}, "dashboard")

	// The name in the body may be left out; the key says who is asking.
	code, out := h.post("/_core/config/db", map[string]string{"api_key": key})
	if code != http.StatusOK || out["dsn"] != "postgres://school:pw@h/db" {
		t.Fatalf("own key: %d %v", code, out)
	}
	// Naming another plugin with this key is refused, not answered.
	if code, out := h.post("/_core/config/db", map[string]string{"api_key": key, "plugin": "accounting"}); code != http.StatusForbidden {
		t.Fatalf("schoolyze's key read accounting's DSN: %d %v", code, out)
	}
	if code, _ := h.post("/_core/config/settings", map[string]string{"api_key": key, "plugin": "accounting"}); code != http.StatusForbidden {
		t.Fatalf("settings: %d", code)
	}
}

func TestMaintenanceWithOwnKey(t *testing.T) {
	h := newCPHarness(t, nil, true)
	school := h.issue("schoolyze")
	identity := h.issue("identity")
	window := func(key, plugin string) int {
		code, _ := h.post("/_core/maintenance", map[string]any{"api_key": key, "plugin": plugin, "on": true, "reason": "test", "seconds": 60})
		return code
	}
	if code := window(school, "schoolyze"); code != http.StatusOK {
		t.Errorf("itself: %d", code)
	}
	if code := window(school, "accounting"); code != http.StatusForbidden {
		t.Errorf("schoolyze held accounting off its tables: %d", code)
	}
	if code := window(identity, "accounting"); code != http.StatusOK {
		t.Errorf("identity runs every plugin's migrations and must be able to: %d", code)
	}
}

func TestSharedKeyTurnedOff(t *testing.T) {
	h := newCPHarness(t, nil, true)
	key := h.issue("schoolyze")
	if err := h.store.SetAcceptSharedKey(context.Background(), false, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.registerWith("accounting", "plugin-key"); code != http.StatusUnauthorized {
		t.Fatalf("shared key still accepted for register: %d", code)
	}
	if code, _ := h.post("/_core/config/db", map[string]string{"api_key": "plugin-key", "plugin": "accounting"}); code != http.StatusUnauthorized {
		t.Fatalf("shared key still accepted for config: %d", code)
	}
	if code, out := h.registerWith("schoolyze", key); code != http.StatusOK {
		t.Fatalf("own key stopped working with the shared key off: %d %v", code, out)
	}
}

func TestRevokedKeyIsRefused(t *testing.T) {
	h := newCPHarness(t, nil, true)
	ctx := context.Background()
	raw, k, _ := h.store.IssuePluginKey(ctx, "schoolyze", "dashboard")
	h.store.RevokePluginKey(ctx, "schoolyze", k.ID, "dashboard")
	// A revoked key is not the shared key either, and the refusal says which
	// it is, so nobody goes looking at the shared key.
	code, out := h.registerWith("schoolyze", raw)
	if code != http.StatusUnauthorized || !strings.Contains(out["error"].(string), "revoked") {
		t.Fatalf("revoked key: %d %v", code, out)
	}
}
