package main

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/store"
)

// storeLimits resolves each plugin's protection limits from the store, over
// cfg's default (the environment and CONFIG_FILE). If the store cannot be
// read the plugin gets cfg's limits, as it would have before the store held
// any: a registration must not fail, and a plugin must not run unlimited,
// over a config read.
func storeLimits(st *store.Store, cfg config.Config) dispatcher.LimitsSource {
	return func(name string) (config.Limits, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		eff, err := st.EffectiveProtection(ctx, name, cfg.Default)
		if err != nil {
			log.Printf("[warn] protection limits for %s not read from the store, using Core's config: %v", name, err)
			_, own := cfg.Plugins[name]
			return cfg.For(name), own
		}
		return eff.Limits, eff.OwnRate
	}
}

// seedProtectionLimits copies CONFIG_FILE's per-plugin overrides into the
// store for plugins that have none there yet, and says which ones the file
// no longer decides — the dashboard owns a plugin once it has history.
func seedProtectionLimits(ctx context.Context, st *store.Store, cfg config.Config) {
	if len(cfg.Plugins) == 0 {
		return
	}
	seeded, differ, err := st.SeedProtectionLimits(ctx, cfg.Plugins)
	sort.Strings(seeded)
	sort.Strings(differ)
	if len(seeded) > 0 {
		log.Printf("[store] protection limits seeded from CONFIG_FILE for %s", strings.Join(seeded, ", "))
	}
	if len(differ) > 0 {
		log.Printf("[warn] CONFIG_FILE limits for %s are ignored: those plugins' limits are set in the dashboard now", strings.Join(differ, ", "))
	}
	if err != nil {
		// Not fatal: the plugins left unseeded run on the default until
		// someone sets them in the dashboard.
		log.Printf("[warn] protection limits not seeded from CONFIG_FILE: %v", err)
	}
}
