package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPluginKeyLifecycle(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()

	raw, k, err := s.IssuePluginKey(ctx, "schoolyze", "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "akx_") || len(raw) < 40 || !strings.HasSuffix(raw, k.Hint) {
		t.Fatalf("key %q hint %q", raw, k.Hint)
	}
	plugin, ok, err := s.LookupPluginKey(ctx, raw)
	if err != nil || !ok || plugin != "schoolyze" {
		t.Fatalf("lookup: %q %v %v", plugin, ok, err)
	}
	list, _ := s.ListPluginKeys(ctx)
	if len(list) != 1 || list[0].LastUsedAt == nil {
		t.Fatalf("last use not recorded: %+v", list)
	}

	// The store holds no key that could be used: only its hash.
	var stored string
	s.db.QueryRow(`SELECT key_hash FROM plugin_keys`).Scan(&stored)
	if strings.Contains(stored, raw[4:]) {
		t.Fatal("the key itself is in the store")
	}

	if err := s.RevokePluginKey(ctx, "accounting", k.ID, "dashboard"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked through another plugin's name: %v", err)
	}
	if err := s.RevokePluginKey(ctx, "schoolyze", k.ID, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.LookupPluginKey(ctx, raw); ok {
		t.Fatal("a revoked key still works")
	}
}

func TestLookupRefusesWhatIsNotAKey(t *testing.T) {
	s := openTest(t, nil)
	for _, raw := range []string{"", "akx_", "change-me-plugin-key", "akx_unknownunknownunknown"} {
		if _, ok, err := s.LookupPluginKey(context.Background(), raw); ok || err != nil {
			t.Errorf("%q: ok=%v err=%v", raw, ok, err)
		}
	}
}

func TestTwoKeysAtOnceForReplacement(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	oldRaw, oldKey, _ := s.IssuePluginKey(ctx, "schoolyze", "dashboard")
	newRaw, _, _ := s.IssuePluginKey(ctx, "schoolyze", "dashboard")
	for _, raw := range []string{oldRaw, newRaw} {
		if p, ok, _ := s.LookupPluginKey(ctx, raw); !ok || p != "schoolyze" {
			t.Fatal("both keys must work until the old one is revoked")
		}
	}
	s.RevokePluginKey(ctx, "schoolyze", oldKey.ID, "dashboard")
	if _, ok, _ := s.LookupPluginKey(ctx, newRaw); !ok {
		t.Fatal("revoking the old key broke the new one")
	}
}

func TestAcceptSharedKeyDefaultsOn(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	if on, _ := s.AcceptSharedKey(ctx); !on {
		t.Fatal("a fresh store refuses the shared key; every plugin would be locked out")
	}
	s.SetAcceptSharedKey(ctx, false, "dashboard")
	if on, _ := s.AcceptSharedKey(ctx); on {
		t.Fatal("not turned off")
	}
	s.SetAcceptSharedKey(ctx, true, "dashboard")
	if on, _ := s.AcceptSharedKey(ctx); !on {
		t.Fatal("not turned back on")
	}
}
