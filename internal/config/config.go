// Package config loads gateway protection/resilience settings.
// Defaults are sane for production; override globally via env or per-plugin via
// an optional YAML file (CONFIG_FILE).
package config

import (
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Limits holds the tunable protection knobs for a plugin (or the global default).
type Limits struct {
	RatePerSec     float64       `yaml:"rate_per_sec"`
	RateBurst      float64       `yaml:"rate_burst"`
	BulkheadMax    int           `yaml:"bulkhead_max"`
	CBThreshold    int           `yaml:"cb_threshold"`
	CBResetTimeout time.Duration `yaml:"cb_reset_timeout"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
}

// Config is the full gateway config: a global default + per-plugin overrides.
type Config struct {
	Default        Limits            `yaml:"default"`
	Plugins        map[string]Limits `yaml:"plugins"`
	HealthInterval time.Duration     `yaml:"health_interval"`
}

// Default returns built-in defaults (used when no file/env overrides apply).
func Defaults() Config {
	return Config{
		Default: Limits{
			RatePerSec:     1000,
			RateBurst:      1000,
			BulkheadMax:    100,
			CBThreshold:    5,
			CBResetTimeout: 30 * time.Second,
			RequestTimeout: 30 * time.Second,
		},
		Plugins:        map[string]Limits{},
		HealthInterval: 30 * time.Second,
	}
}

// Load builds config from defaults, then an optional YAML file (CONFIG_FILE),
// then env-var overrides for the global default.
func Load() (Config, error) {
	cfg := Defaults()

	if path := os.Getenv("CONFIG_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return cfg, err
		}
		var fileCfg Config
		if err := yaml.Unmarshal(b, &fileCfg); err != nil {
			return cfg, err
		}
		mergeFile(&cfg, fileCfg)
	}

	applyEnv(&cfg.Default)
	if v := envDuration("HEALTH_INTERVAL"); v > 0 {
		cfg.HealthInterval = v
	}
	return cfg, nil
}

// For returns the effective limits for a plugin: per-plugin override if present,
// else the global default.
func (c Config) For(pluginName string) Limits {
	if l, ok := c.Plugins[pluginName]; ok {
		return withDefaults(l, c.Default)
	}
	return c.Default
}

// --- merging helpers ---

func mergeFile(cfg *Config, file Config) {
	cfg.Default = withDefaults(file.Default, cfg.Default)
	if file.HealthInterval > 0 {
		cfg.HealthInterval = file.HealthInterval
	}
	if cfg.Plugins == nil {
		cfg.Plugins = map[string]Limits{}
	}
	for name, l := range file.Plugins {
		cfg.Plugins[name] = l
	}
}

// withDefaults fills zero fields in l from def.
func withDefaults(l, def Limits) Limits {
	if l.RatePerSec == 0 {
		l.RatePerSec = def.RatePerSec
	}
	if l.RateBurst == 0 {
		l.RateBurst = def.RateBurst
	}
	if l.BulkheadMax == 0 {
		l.BulkheadMax = def.BulkheadMax
	}
	if l.CBThreshold == 0 {
		l.CBThreshold = def.CBThreshold
	}
	if l.CBResetTimeout == 0 {
		l.CBResetTimeout = def.CBResetTimeout
	}
	if l.RequestTimeout == 0 {
		l.RequestTimeout = def.RequestTimeout
	}
	return l
}

func applyEnv(l *Limits) {
	if v := envFloat("RATE_PER_SEC"); v > 0 {
		l.RatePerSec = v
		if l.RateBurst < v {
			l.RateBurst = v
		}
	}
	if v := envInt("BULKHEAD_MAX"); v > 0 {
		l.BulkheadMax = v
	}
	if v := envInt("CB_THRESHOLD"); v > 0 {
		l.CBThreshold = v
	}
	if v := envDuration("CB_RESET_TIMEOUT"); v > 0 {
		l.CBResetTimeout = v
	}
	if v := envDuration("REQUEST_TIMEOUT"); v > 0 {
		l.RequestTimeout = v
	}
}

func envFloat(k string) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return 0
}
func envInt(k string) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return 0
}
func envDuration(k string) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(k)); err == nil {
		return v
	}
	return 0
}
