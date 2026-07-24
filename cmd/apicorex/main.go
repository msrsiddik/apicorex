package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/msrsiddik/apicorex/internal/auth"
	"github.com/msrsiddik/apicorex/internal/config"
	"github.com/msrsiddik/apicorex/internal/controlplane"
	"github.com/msrsiddik/apicorex/internal/dispatcher"
	"github.com/msrsiddik/apicorex/internal/openapi"
	"github.com/msrsiddik/apicorex/internal/protection"
	"github.com/msrsiddik/apicorex/internal/registry"
	"github.com/msrsiddik/apicorex/internal/tracing"
	"github.com/msrsiddik/apicorex/server"
	"golang.org/x/sync/errgroup"
)

func main() {
	godotenv.Load()

	httpAddr := envOr("HTTP_PORT", ":8080")
	pluginAPIKey := envOr("PLUGIN_API_KEY", "")
	allowlist := splitCSV(envOr("PLUGIN_ALLOWLIST", "")) // empty = allow any (dev)
	dashboardSecret := envOr("DASHBOARD_SECRET", "")     // empty = dashboard login disabled (dev)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx0 := context.Background()
	traceShutdown, tracingOn, err := tracing.Init(ctx0)
	if err != nil {
		log.Fatalf("init tracing: %v", err)
	}
	if tracingOn {
		log.Printf("[core] distributed tracing enabled (OTLP)")
		defer traceShutdown(ctx0)
	}

	reg := registry.New()
	cb := protection.NewCircuitBreaker(cfg.Default.CBThreshold, cfg.Default.CBResetTimeout)
	bh := protection.NewBulkhead(cfg.Default.BulkheadMax)

	injector := openapi.NewInjector()
	disp := dispatcher.New(reg, cb, bh, cfg)

	// Device tokens are resolved via Identity's /internal/introspect, guarded by
	// the shared PLUGIN_API_KEY. Without it auth is disabled (dev only).
	var introspector *auth.Introspector
	if pluginAPIKey != "" {
		introspector = auth.NewIntrospector(reg, pluginAPIKey)
	} else {
		log.Println("[warn] PLUGIN_API_KEY not set — auth middleware disabled")
	}

	if dashboardSecret == "" {
		log.Println("[warn] DASHBOARD_SECRET not set — gateway dashboard login disabled")
	}
	cpHandlers := controlplane.New(reg, disp, injector, pluginAPIKey, allowlist, pluginAPIKey, dashboardSecret)
	httpSrv := server.NewHTTP(reg, disp, injector, introspector, cpHandlers, serveDashboard, httpAddr)

	healthMon := protection.NewHealthMonitor(reg, cb, cfg.HealthInterval)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	g, gCtx := errgroup.WithContext(ctx)

	g.Go(func() error { return httpSrv.Start(gCtx) })
	g.Go(func() error {
		healthMon.Run(gCtx)
		return nil
	})
	g.Go(func() error {
		<-gCtx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutCtx)
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatalf("server error: %v", err)
	}
	log.Println("shutdown complete")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
