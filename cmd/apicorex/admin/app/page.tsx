"use client";

import { useCallback, useEffect, useState } from "react";
import Sidebar, { type SectionId } from "@/components/Sidebar";
import OverviewPanel from "@/components/OverviewPanel";
import PluginsPanel from "@/components/PluginsPanel";
import LoginForm from "@/components/LoginForm";
import {
  checkSession,
  fetchMetrics,
  fetchPlugins,
  loginRequired,
  type MetricFamilies,
  type Plugin,
} from "@/lib/api";

const POLL_MS = 10_000;

type AuthState = "checking" | "authenticated" | "unauthenticated";

// The dashboard is a statically exported SPA served under basePath
// "/dashboard" (see cmd/apicorex/dashboard.go). /dashboard and / both render
// this same overview page; /plugin is a second, explicit Go route serving
// the identical index.html (its JS/CSS still load from /dashboard/_next/...,
// since basePath only affects asset URLs, not this route) so the plugins tab
// gets its own top-level URL instead of nesting under /dashboard.
function sectionFromPath(pathname: string): SectionId {
  return pathname.replace(/\/+$/, "") === "/plugin" ? "plugins" : "overview";
}

export default function Home() {
  const [auth, setAuth] = useState<AuthState>("checking");
  const [section, setSectionState] = useState<SectionId>("overview");

  useEffect(() => {
    setSectionState(sectionFromPath(window.location.pathname));
    const onPopState = () => setSectionState(sectionFromPath(window.location.pathname));
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const setSection = useCallback((id: SectionId) => {
    setSectionState(id);
    const path = id === "overview" ? "/dashboard" : "/plugin";
    if (window.location.pathname.replace(/\/+$/, "") !== path) {
      window.history.pushState(null, "", path);
    }
  }, []);
  const [plugins, setPlugins] = useState<Plugin[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [metrics, setMetrics] = useState<MetricFamilies | null>(null);
  const [metricsError, setMetricsError] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      const required = await loginRequired().catch(() => false);
      if (!required) {
        setAuth("authenticated");
        return;
      }
      const valid = await checkSession();
      setAuth(valid ? "authenticated" : "unauthenticated");
    })();
  }, []);

  const load = useCallback(async () => {
    try {
      const data = await fetchPlugins();
      setPlugins(data);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }

    try {
      const m = await fetchMetrics();
      setMetrics(m);
      setMetricsError(null);
    } catch (e) {
      setMetricsError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    if (auth !== "authenticated") return;
    load();
    const id = setInterval(load, POLL_MS);
    return () => clearInterval(id);
  }, [auth, load]);

  if (auth === "checking") {
    return <div className="h-screen bg-background" />;
  }

  if (auth === "unauthenticated") {
    return <LoginForm onSuccess={() => setAuth("authenticated")} />;
  }

  return (
    <div className="flex h-screen">
      <Sidebar
        active={section}
        onNavigate={setSection}
        onLogout={() => setAuth("unauthenticated")}
      />
      <main className="flex-1 overflow-y-auto p-6">
        {section === "overview" ? (
          <OverviewPanel
            plugins={plugins}
            loading={loading}
            error={error}
            metrics={metrics}
            metricsError={metricsError}
          />
        ) : (
          <PluginsPanel plugins={plugins} loading={loading} error={error} onRefresh={load} />
        )}
      </main>
    </div>
  );
}
