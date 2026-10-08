package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func ip(n int) *int { return &n }

func mustSave(t *testing.T, s *Store, plugin string, in DBConfigInput) DBConfig {
	t.Helper()
	c, err := s.SaveDBConfig(context.Background(), plugin, in, "dashboard")
	if err != nil {
		t.Fatalf("save %s: %v", plugin, err)
	}
	return c
}

func TestEffectiveInheritsFieldByField(t *testing.T) {
	s := openTest(t, testKey(t))
	ctx := context.Background()
	mustSave(t, s, DefaultPlugin, DBConfigInput{
		DSNAction: DSNSet, DSN: "postgres://app:pw@db:5432/apicorex",
		Pool: PoolSettings{MaxOpen: ip(20), MaxIdle: ip(4)},
	})
	mustSave(t, s, "schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(25)}})

	eff, err := s.EffectiveDBConfig(ctx, "schoolyze")
	if err != nil {
		t.Fatal(err)
	}
	if eff.DSN != "postgres://app:pw@db:5432/apicorex" {
		t.Errorf("dsn not inherited: %q", eff.DSN)
	}
	if eff.MaxOpen != 25 || eff.MaxIdle != 4 {
		t.Errorf("pool: open %d idle %d, want 25 and 4", eff.MaxOpen, eff.MaxIdle)
	}
	if eff.ConnMaxLifetime != builtinConnMaxLifetime || eff.ConnMaxIdleTime != builtinConnMaxIdleTime {
		t.Errorf("unset fields did not fall to built-ins: %v %v", eff.ConnMaxLifetime, eff.ConnMaxIdleTime)
	}

	// A plugin with no row of its own gets the default whole.
	other, err := s.EffectiveDBConfig(ctx, "accounting")
	if err != nil || other.MaxOpen != 20 {
		t.Fatalf("plugin without a row: %+v %v", other, err)
	}
}

func TestEffectiveOwnDSNWins(t *testing.T) {
	s := openTest(t, testKey(t))
	mustSave(t, s, DefaultPlugin, DBConfigInput{DSNAction: DSNSet, DSN: "postgres://a:a@shared/db"})
	mustSave(t, s, "accounting", DBConfigInput{DSNAction: DSNSet, DSN: "postgres://ledger:x@shared/db"})
	eff, err := s.EffectiveDBConfig(context.Background(), "accounting")
	if err != nil || eff.DSN != "postgres://ledger:x@shared/db" {
		t.Fatalf("%+v %v", eff, err)
	}
}

func TestEffectiveWithoutAnyDSN(t *testing.T) {
	s := openTest(t, testKey(t))
	if _, err := s.EffectiveDBConfig(context.Background(), "schoolyze"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestEffectiveCapsIdleAtOpen(t *testing.T) {
	s := openTest(t, testKey(t))
	mustSave(t, s, DefaultPlugin, DBConfigInput{DSNAction: DSNSet, DSN: "postgres://a:a@h/db", Pool: PoolSettings{MaxIdle: ip(8)}})
	mustSave(t, s, "zumo-pos", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(3)}})
	eff, err := s.EffectiveDBConfig(context.Background(), "zumo-pos")
	if err != nil || eff.MaxIdle != 3 {
		t.Fatalf("idle %d, want capped to 3 (%v)", eff.MaxIdle, err)
	}
}

func TestVersionChangesWithEitherRow(t *testing.T) {
	// The plugin learns of a change by its version differing; a change to the
	// default must reach plugins that only inherit, and removing an override
	// must register too — it leaves no row behind, only history.
	s := openTest(t, testKey(t))
	ctx := context.Background()
	eff := func() int64 {
		e, err := s.EffectiveDBConfig(ctx, "schoolyze")
		if err != nil {
			t.Fatal(err)
		}
		return e.Version
	}
	mustSave(t, s, DefaultPlugin, DBConfigInput{DSNAction: DSNSet, DSN: "postgres://a:a@h/db"})
	v1 := eff()
	mustSave(t, s, "schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(5)}})
	v2 := eff()
	mustSave(t, s, DefaultPlugin, DBConfigInput{DSNAction: DSNKeep, Pool: PoolSettings{MaxIdle: ip(1)}})
	v3 := eff()
	if err := s.DeleteDBConfig(ctx, "schoolyze", "dashboard"); err != nil {
		t.Fatal(err)
	}
	v4 := eff()
	if !(v1 < v2 && v2 < v3 && v3 < v4) {
		t.Fatalf("version did not rise on every change: %d %d %d %d", v1, v2, v3, v4)
	}
}

func TestKeepLeavesDSNAlone(t *testing.T) {
	s := openTest(t, testKey(t))
	mustSave(t, s, "schoolyze", DBConfigInput{DSNAction: DSNSet, DSN: "postgres://u:secret@h/db"})
	c := mustSave(t, s, "schoolyze", DBConfigInput{DSNAction: DSNKeep, Pool: PoolSettings{MaxOpen: ip(7)}})
	if !c.HasDSN || c.Pool.MaxOpen == nil || *c.Pool.MaxOpen != 7 {
		t.Fatalf("%+v", c)
	}
	eff, err := s.EffectiveDBConfig(context.Background(), "schoolyze")
	if err != nil || eff.DSN != "postgres://u:secret@h/db" {
		t.Fatalf("dsn lost on a pool-only save: %+v %v", eff, err)
	}
}

func TestInheritClearsOwnDSN(t *testing.T) {
	s := openTest(t, testKey(t))
	mustSave(t, s, DefaultPlugin, DBConfigInput{DSNAction: DSNSet, DSN: "postgres://d:d@h/db"})
	mustSave(t, s, "schoolyze", DBConfigInput{DSNAction: DSNSet, DSN: "postgres://own:own@h/db"})
	c := mustSave(t, s, "schoolyze", DBConfigInput{DSNAction: DSNInherit})
	if c.HasDSN {
		t.Fatal("still has its own dsn")
	}
	eff, _ := s.EffectiveDBConfig(context.Background(), "schoolyze")
	if eff.DSN != "postgres://d:d@h/db" {
		t.Fatalf("did not fall back to the default: %q", eff.DSN)
	}
}

func TestListNeverCarriesThePassword(t *testing.T) {
	s := openTest(t, testKey(t))
	mustSave(t, s, "schoolyze", DBConfigInput{DSNAction: DSNSet, DSN: "postgres://u:hunter2@h:5432/db?sslmode=disable"})
	list, err := s.ListDBConfigs(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if strings.Contains(list[0].DSNDisplay, "hunter2") {
		t.Fatalf("password in display form: %s", list[0].DSNDisplay)
	}
	if !strings.Contains(list[0].DSNDisplay, "h:5432") {
		t.Errorf("display lost the host: %s", list[0].DSNDisplay)
	}
	audit, _ := s.ListAudit(context.Background(), 10, 0)
	for _, a := range audit {
		if strings.Contains(a.Detail, "hunter2") {
			t.Fatalf("password in audit trail: %s", a.Detail)
		}
	}
	hist, _ := s.DBConfigHistory(context.Background(), "schoolyze", 10)
	if len(hist) != 1 || strings.Contains(hist[0].DSNDisplay, "hunter2") {
		t.Fatalf("history: %+v", hist)
	}
}

func TestListPutsDefaultFirst(t *testing.T) {
	s := openTest(t, testKey(t))
	mustSave(t, s, "accounting", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(5)}})
	mustSave(t, s, DefaultPlugin, DBConfigInput{Pool: PoolSettings{MaxOpen: ip(5)}})
	list, _ := s.ListDBConfigs(context.Background())
	if len(list) != 2 || list[0].Plugin != DefaultPlugin {
		t.Fatalf("%+v", list)
	}
}

func TestRollbackRestoresAndIsItselfAVersion(t *testing.T) {
	s := openTest(t, testKey(t))
	ctx := context.Background()
	good := mustSave(t, s, "schoolyze", DBConfigInput{DSNAction: DSNSet, DSN: "postgres://u:good@h/db", Pool: PoolSettings{MaxOpen: ip(10)}})
	mustSave(t, s, "schoolyze", DBConfigInput{DSNAction: DSNSet, DSN: "postgres://u:typo@wrong/db", Pool: PoolSettings{MaxOpen: ip(99)}})

	c, err := s.RollbackDBConfig(ctx, "schoolyze", good.Version, "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if c.Version == good.Version {
		t.Error("rollback reused the old version number; plugins would not see the change")
	}
	eff, err := s.EffectiveDBConfig(ctx, "schoolyze")
	if err != nil || eff.DSN != "postgres://u:good@h/db" || eff.MaxOpen != 10 {
		t.Fatalf("not restored: %+v %v", eff, err)
	}
	hist, _ := s.DBConfigHistory(ctx, "schoolyze", 10)
	if len(hist) != 3 || hist[0].Action != "rollback" {
		t.Fatalf("history: %+v", hist)
	}
}

func TestRollbackToADelete(t *testing.T) {
	s := openTest(t, testKey(t))
	ctx := context.Background()
	mustSave(t, s, "schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(5)}})
	if err := s.DeleteDBConfig(ctx, "schoolyze", "dashboard"); err != nil {
		t.Fatal(err)
	}
	hist, _ := s.DBConfigHistory(ctx, "schoolyze", 10)
	deleted := hist[0].Version
	mustSave(t, s, "schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(6)}})
	if _, err := s.RollbackDBConfig(ctx, "schoolyze", deleted, "dashboard"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDBConfig(ctx, "schoolyze"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("row still present after rolling back to a delete: %v", err)
	}
}

func TestRollbackRefusesAnotherPluginsVersion(t *testing.T) {
	// A version number from another plugin's history must not be applied:
	// its sealed DSN is bound to that plugin and would not open, and its pool
	// was sized for something else.
	s := openTest(t, testKey(t))
	other := mustSave(t, s, "accounting", DBConfigInput{DSNAction: DSNSet, DSN: "postgres://l:l@h/db"})
	mustSave(t, s, "schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(5)}})
	if _, err := s.RollbackDBConfig(context.Background(), "schoolyze", other.Version, "dashboard"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	s := openTest(t, testKey(t))
	ctx := context.Background()
	cases := map[string]struct {
		plugin string
		in     DBConfigInput
	}{
		"bad plugin name":      {"School Yze", DBConfigInput{}},
		"key=value dsn":        {"schoolyze", DBConfigInput{DSNAction: DSNSet, DSN: "host=db user=app"}},
		"mysql dsn":            {"schoolyze", DBConfigInput{DSNAction: DSNSet, DSN: "mysql://u:p@h/db"}},
		"dsn without host":     {"schoolyze", DBConfigInput{DSNAction: DSNSet, DSN: "postgres:///db"}},
		"zero max_open":        {"schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(0)}}},
		"huge max_open":        {"schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(5000)}}},
		"idle above open":      {"schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(2), MaxIdle: ip(3)}}},
		"negative lifetime":    {"schoolyze", DBConfigInput{Pool: PoolSettings{ConnMaxLifetimeSec: ip(-1)}}},
		"unknown dsn action":   {"schoolyze", DBConfigInput{DSNAction: "replace"}},
		"default inherits dsn": {DefaultPlugin, DBConfigInput{DSNAction: DSNInherit}},
	}
	for name, c := range cases {
		if _, err := s.SaveDBConfig(ctx, c.plugin, c.in, "dashboard"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	if err := s.DeleteDBConfig(ctx, DefaultPlugin, "dashboard"); !errors.Is(err, ErrInvalid) {
		t.Errorf("deleting the default: %v", err)
	}
}

func TestSaveDSNWithoutMasterKey(t *testing.T) {
	s := openTest(t, nil)
	_, err := s.SaveDBConfig(context.Background(), "schoolyze", DBConfigInput{DSNAction: DSNSet, DSN: "postgres://u:p@h/db"}, "dashboard")
	if !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("want ErrNoMasterKey, got %v", err)
	}
	// Pool-only rows need no key.
	if _, err := s.SaveDBConfig(context.Background(), "schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(3)}}, "dashboard"); err != nil {
		t.Fatal(err)
	}
}

func TestSaveIsAudited(t *testing.T) {
	s := openTest(t, testKey(t))
	mustSave(t, s, "schoolyze", DBConfigInput{Pool: PoolSettings{MaxOpen: ip(3)}, Note: "more traffic"})
	a, _ := s.ListAudit(context.Background(), 1, 0)
	if len(a) != 1 || a[0].Action != "db_config.save" || a[0].Target != "schoolyze" || !strings.Contains(a[0].Detail, "more traffic") {
		t.Fatalf("%+v", a)
	}
	if a[0].At.Before(time.Now().Add(-time.Minute)) {
		t.Error("timestamp is not now")
	}
}

func TestResolveWithoutMasterKeyStillReportsPool(t *testing.T) {
	// The dashboard lists every plugin's pool even on a Core started without
	// its key; only opening the DSN needs it.
	path := t.TempDir() + "/core.db"
	ctx := context.Background()
	key := testKey(t)
	s, err := Open(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, DefaultPlugin, DBConfigInput{DSNAction: DSNSet, DSN: "postgres://a:a@h/db", Pool: PoolSettings{MaxOpen: ip(15)}})
	s.Close()

	s, err = Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	eff, err := s.ResolveDBConfig(ctx, "schoolyze")
	if err != nil || eff.MaxOpen != 15 || eff.DSNSource != DefaultPlugin || eff.DSN != "" {
		t.Fatalf("%+v %v", eff, err)
	}
	if _, err := s.EffectiveDBConfig(ctx, "schoolyze"); !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("opened a DSN without the key: %v", err)
	}
}
