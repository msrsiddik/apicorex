package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/msrsiddik/apicorex/internal/config"
)

func fp(v float64) *float64 { return &v }
func i64p(v int64) *int64   { return &v }

var protBase = config.Defaults().Default

func mustSaveProtection(t *testing.T, s *Store, plugin string, f ProtectionFields) {
	t.Helper()
	if err := s.SaveProtectionLimits(context.Background(), plugin, f, "", "dashboard"); err != nil {
		t.Fatalf("save %s: %v", plugin, err)
	}
}

func effective(t *testing.T, s *Store, plugin string) EffectiveProtection {
	t.Helper()
	eff, err := s.EffectiveProtection(context.Background(), plugin, protBase)
	if err != nil {
		t.Fatal(err)
	}
	return eff
}

func TestProtectionInheritsFieldByField(t *testing.T) {
	s := openTest(t, nil)
	mustSaveProtection(t, s, DefaultPlugin, ProtectionFields{BulkheadMax: ip(40), RequestTimeoutMs: i64p(60_000)})
	mustSaveProtection(t, s, "billing", ProtectionFields{BulkheadMax: ip(10), RatePerSec: fp(50)})

	eff := effective(t, s, "billing")
	l := eff.Limits
	if l.BulkheadMax != 10 || l.RatePerSec != 50 {
		t.Errorf("own fields: bulkhead %d rate %g", l.BulkheadMax, l.RatePerSec)
	}
	if l.RequestTimeout != time.Minute {
		t.Errorf("timeout %v, want the default row's minute", l.RequestTimeout)
	}
	if l.CBThreshold != protBase.CBThreshold || l.RateBurst != protBase.RateBurst {
		t.Errorf("unset fields should come from the config: %+v", l)
	}
	want := map[string]string{"bulkhead_max": "billing", "request_timeout_ms": SourceDefault, "cb_threshold": SourceConfig}
	for k, v := range want {
		if eff.Source[k] != v {
			t.Errorf("source of %s: %q, want %q", k, eff.Source[k], v)
		}
	}
	if !eff.OwnRate {
		t.Error("billing sets its own rate")
	}

	other := effective(t, s, "schoolyze")
	if other.Limits.BulkheadMax != 40 || other.OwnRate {
		t.Errorf("a plugin without a row gets the default row: %+v own %v", other.Limits, other.OwnRate)
	}
}

func TestProtectionVersionMovesWithTheDefault(t *testing.T) {
	s := openTest(t, nil)
	mustSaveProtection(t, s, "billing", ProtectionFields{BulkheadMax: ip(10)})
	v1 := effective(t, s, "billing").Version
	mustSaveProtection(t, s, DefaultPlugin, ProtectionFields{CBThreshold: ip(3)})
	v2 := effective(t, s, "billing").Version
	if v2 <= v1 {
		t.Errorf("version %d after a default change, was %d", v2, v1)
	}
	if err := s.DeleteProtectionLimits(context.Background(), "billing", "dashboard"); err != nil {
		t.Fatal(err)
	}
	if v3 := effective(t, s, "billing").Version; v3 <= v2 {
		t.Errorf("version %d after a delete, was %d", v3, v2)
	}
}

func TestProtectionValidation(t *testing.T) {
	s := openTest(t, nil)
	for name, f := range map[string]ProtectionFields{
		"rate_per_sec":        {RatePerSec: fp(0)},
		"rate_burst":          {RateBurst: fp(0.5)},
		"tenant_rate_per_sec": {TenantRatePerSec: fp(-1)},
		"bulkhead_max":        {BulkheadMax: ip(0)},
		"cb_threshold":        {CBThreshold: ip(maxCBThreshold + 1)},
		"cb_reset_timeout_ms": {CBResetTimeoutMs: i64p(10)},
		"request_timeout_ms":  {RequestTimeoutMs: i64p(500)},
	} {
		err := s.SaveProtectionLimits(context.Background(), "billing", f, "", "dashboard")
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v, want an ErrInvalid naming the field", name, err)
		}
	}
	// Zero turns a tenant sub-limit off; that has to be allowed.
	mustSaveProtection(t, s, "billing", ProtectionFields{TenantRatePerSec: fp(0)})
	if err := s.SaveProtectionLimits(context.Background(), "Bad Name", ProtectionFields{}, "", "x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad plugin name: %v", err)
	}
}

func TestProtectionRollbackAndHistory(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	mustSaveProtection(t, s, "billing", ProtectionFields{BulkheadMax: ip(10)})
	mustSaveProtection(t, s, "billing", ProtectionFields{BulkheadMax: ip(20)})
	if err := s.DeleteProtectionLimits(ctx, "billing", "dashboard"); err != nil {
		t.Fatal(err)
	}
	h, err := s.ProtectionHistory(ctx, "billing", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 3 || h[0].Action != "delete" || *h[1].Limits.BulkheadMax != 20 {
		t.Fatalf("history newest first: %+v", h)
	}

	if err := s.RollbackProtectionLimits(ctx, "billing", h[2].Version, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if got := effective(t, s, "billing").Limits.BulkheadMax; got != 10 {
		t.Errorf("after rollback to the first save: %d", got)
	}
	// Back to the delete: the row goes again.
	if err := s.RollbackProtectionLimits(ctx, "billing", h[0].Version, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if got := effective(t, s, "billing").Limits.BulkheadMax; got != protBase.BulkheadMax {
		t.Errorf("after rollback to the delete: %d", got)
	}
	if err := s.RollbackProtectionLimits(ctx, "schoolyze", h[2].Version, "dashboard"); !errors.Is(err, ErrNotFound) {
		t.Errorf("another plugin's version: %v", err)
	}
	if err := s.DeleteProtectionLimits(ctx, "billing", "dashboard"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete with no row: %v", err)
	}

	entries, err := s.ListAudit(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 || entries[0].Action != "protection.rollback" || entries[4].Detail != "bulkhead_max 10" {
		t.Errorf("audit: %+v", entries)
	}
}

func TestSeedProtectionLimits(t *testing.T) {
	s := openTest(t, nil)
	ctx := context.Background()
	mustSaveProtection(t, s, "billing", ProtectionFields{BulkheadMax: ip(99)})

	file := map[string]config.Limits{
		"billing":   {BulkheadMax: 5},
		"schoolyze": {CBThreshold: 9, RequestTimeout: 90 * time.Second},
		"empty":     {},
	}
	seeded, differ, err := s.SeedProtectionLimits(ctx, file)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seeded, []string{"schoolyze"}) || !slices.Equal(differ, []string{"billing"}) {
		t.Errorf("seeded %v differ %v", seeded, differ)
	}
	if got := effective(t, s, "billing").Limits.BulkheadMax; got != 99 {
		t.Errorf("the dashboard's billing row was overwritten: %d", got)
	}
	l := effective(t, s, "schoolyze").Limits
	if l.CBThreshold != 9 || l.RequestTimeout != 90*time.Second || l.BulkheadMax != protBase.BulkheadMax {
		t.Errorf("seeded schoolyze: %+v", l)
	}

	// A second start seeds nothing and, with the file unchanged, warns of
	// nothing for the plugin it seeded.
	seeded, differ, err = s.SeedProtectionLimits(ctx, file)
	if err != nil || len(seeded) != 0 || !slices.Equal(differ, []string{"billing"}) {
		t.Errorf("second run: seeded %v differ %v err %v", seeded, differ, err)
	}

	if _, _, err := s.SeedProtectionLimits(ctx, map[string]config.Limits{"x": {BulkheadMax: -1}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an invalid file: %v", err)
	}
}
