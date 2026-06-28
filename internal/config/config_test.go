package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	c := Defaults()
	if c.Default.RatePerSec != 1000 || c.Default.BulkheadMax != 100 || c.Default.CBThreshold != 5 {
		t.Fatalf("unexpected defaults: %+v", c.Default)
	}
}

func TestLoad_EnvOverride(t *testing.T) {
	t.Setenv("RATE_PER_SEC", "777")
	t.Setenv("BULKHEAD_MAX", "42")

	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Default.RatePerSec != 777 {
		t.Errorf("RATE_PER_SEC override = %v, want 777", c.Default.RatePerSec)
	}
	if c.Default.BulkheadMax != 42 {
		t.Errorf("BULKHEAD_MAX override = %v, want 42", c.Default.BulkheadMax)
	}
}

func TestLoad_FileWithPerPluginOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	yaml := `
default:
  rate_per_sec: 500
  cb_threshold: 3
plugins:
  billing:
    rate_per_sec: 2000
health_interval: 10s
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)

	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.Default.RatePerSec != 500 {
		t.Errorf("default rate = %v, want 500", c.Default.RatePerSec)
	}
	if c.HealthInterval != 10*time.Second {
		t.Errorf("health_interval = %v, want 10s", c.HealthInterval)
	}
	// per-plugin override wins
	if c.For("billing").RatePerSec != 2000 {
		t.Errorf("billing rate = %v, want 2000", c.For("billing").RatePerSec)
	}
	// billing inherits default cb_threshold
	if c.For("billing").CBThreshold != 3 {
		t.Errorf("billing cb_threshold = %v, want inherited 3", c.For("billing").CBThreshold)
	}
	// unknown plugin falls back to default
	if c.For("unknown").RatePerSec != 500 {
		t.Errorf("unknown rate = %v, want default 500", c.For("unknown").RatePerSec)
	}
}
