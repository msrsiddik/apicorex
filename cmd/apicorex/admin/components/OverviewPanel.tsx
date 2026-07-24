"use client";

import type { MetricFamilies, Plugin } from "@/lib/api";
import { sumMetric } from "@/lib/api";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Plug,
  ShieldCheck,
  ShieldAlert,
  Route as RouteIcon,
  ArrowLeftRight,
  Ban,
  Gauge,
} from "lucide-react";

function StatCard({
  icon: Icon,
  label,
  value,
  loading,
}: {
  icon: typeof Plug;
  label: string;
  value: number;
  loading: boolean;
}) {
  return (
    <div className="flex items-center gap-3 rounded-xl border bg-card px-4 py-3.5">
      <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-accent text-accent-foreground">
        <Icon className="h-5 w-5" />
      </div>
      <div className="flex flex-col">
        <span className="text-xs text-muted-foreground">{label}</span>
        {loading ? (
          <Skeleton className="h-7 w-10" />
        ) : (
          <span className="text-2xl font-semibold tabular-nums">{value}</span>
        )}
      </div>
    </div>
  );
}

export default function OverviewPanel({
  plugins,
  loading,
  error,
  metrics,
  metricsError,
}: {
  plugins: Plugin[];
  loading: boolean;
  error: string | null;
  metrics: MetricFamilies | null;
  metricsError: string | null;
}) {
  const healthy = plugins.filter((p) => p.status === "healthy").length;
  const unhealthy = plugins.length - healthy;
  const totalRoutes = plugins.reduce((n, p) => n + (p.routes?.length ?? 0), 0);

  const requestsTotal = metrics ? sumMetric(metrics, "apicorex_requests_total") : 0;
  const rejectedTotal = metrics ? sumMetric(metrics, "apicorex_requests_rejected_total") : 0;
  const inFlight = metrics ? sumMetric(metrics, "apicorex_requests_in_flight") : 0;
  const metricsLoading = loading && !metrics;

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-lg font-semibold">Overview</h1>
        <p className="text-sm text-muted-foreground">
          Gateway-level status — registered plugins, health, and route counts.
        </p>
      </div>

      {error && (
        <Card>
          <CardContent className="pt-5 text-sm text-danger">
            Failed to load /plugins: {error}
          </CardContent>
        </Card>
      )}

      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <StatCard icon={Plug} label="Registered plugins" value={plugins.length} loading={loading} />
        <StatCard icon={ShieldCheck} label="Healthy" value={healthy} loading={loading} />
        <StatCard icon={ShieldAlert} label="Unhealthy" value={unhealthy} loading={loading} />
        <StatCard icon={RouteIcon} label="Total routes" value={totalRoutes} loading={loading} />
      </div>

      <div>
        <h2 className="mb-3 text-sm font-semibold text-muted-foreground">
          Traffic (since start)
        </h2>
        {metricsError && (
          <Card>
            <CardContent className="pt-5 text-sm text-danger">
              Failed to load /metrics: {metricsError}
            </CardContent>
          </Card>
        )}
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
          <StatCard
            icon={ArrowLeftRight}
            label="Requests proxied"
            value={requestsTotal}
            loading={metricsLoading}
          />
          <StatCard
            icon={Ban}
            label="Rejected (firewall/rate/breaker)"
            value={rejectedTotal}
            loading={metricsLoading}
          />
          <StatCard icon={Gauge} label="In flight now" value={inFlight} loading={metricsLoading} />
        </div>
      </div>
    </div>
  );
}
