"use client";

import { Fragment, useState } from "react";
import { forceDeregister, resetBreaker, type Plugin } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton } from "@/components/ui/skeleton";
import { relativeTime } from "@/lib/utils";
import { ChevronDown, ChevronRight, Plug, RotateCcw, XCircle } from "lucide-react";

export default function PluginsPanel({
  plugins,
  loading,
  error,
  onRefresh,
}: {
  plugins: Plugin[];
  loading: boolean;
  error: string | null;
  onRefresh: () => void;
}) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [busy, setBusy] = useState<Set<string>>(new Set());
  const [actionError, setActionError] = useState<Record<string, string>>({});

  function toggle(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function runAction(id: string, action: () => Promise<void>) {
    setBusy((prev) => new Set(prev).add(id));
    setActionError((prev) => ({ ...prev, [id]: "" }));
    try {
      await action();
      onRefresh();
    } catch (e) {
      setActionError((prev) => ({
        ...prev,
        [id]: e instanceof Error ? e.message : String(e),
      }));
    } finally {
      setBusy((prev) => {
        const next = new Set(prev);
        next.delete(id);
        return next;
      });
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-lg font-semibold">Plugins & routes</h1>
        <p className="text-sm text-muted-foreground">
          Live registry state — every plugin currently registered with Core, and the routes it exposes.
        </p>
      </div>

      {error && (
        <div className="rounded-xl border bg-card px-4 py-3.5 text-sm text-danger">
          Failed to load /plugins: {error}
        </div>
      )}

      {loading ? (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-11 w-full" />
          <Skeleton className="h-11 w-full" />
          <Skeleton className="h-11 w-full" />
        </div>
      ) : plugins.length === 0 ? (
        <EmptyState
          icon={Plug}
          title="No plugins registered"
          description="Nothing has called POST /_core/register yet."
        />
      ) : (
        <div className="rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-8" />
                <TableHead>Plugin</TableHead>
                <TableHead>Version</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>Circuit</TableHead>
                <TableHead>Base URL</TableHead>
                <TableHead>Routes</TableHead>
                <TableHead>Last heartbeat</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {plugins.map((p) => {
                const isOpen = expanded.has(p.plugin_id);
                return (
                  <Fragment key={p.plugin_id}>
                    <TableRow
                      className="cursor-pointer"
                      onClick={() => toggle(p.plugin_id)}
                    >
                      <TableCell>
                        {isOpen ? (
                          <ChevronDown className="h-4 w-4 text-muted-foreground" />
                        ) : (
                          <ChevronRight className="h-4 w-4 text-muted-foreground" />
                        )}
                      </TableCell>
                      <TableCell className="font-medium">{p.plugin_name}</TableCell>
                      <TableCell className="text-muted-foreground">{p.version}</TableCell>
                      <TableCell>
                        <Badge variant={p.status === "healthy" ? "ok" : "danger"}>
                          {p.status}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <Badge
                          variant={
                            p.circuit_state === "closed"
                              ? "ok"
                              : p.circuit_state === "half-open"
                                ? "warn"
                                : "danger"
                          }
                        >
                          {p.circuit_state}
                        </Badge>
                      </TableCell>
                      <TableCell className="text-muted-foreground">{p.base_url}</TableCell>
                      <TableCell className="text-muted-foreground">
                        {p.routes?.length ?? 0}
                      </TableCell>
                      <TableCell className="text-muted-foreground">
                        {relativeTime(p.last_heartbeat)}
                      </TableCell>
                    </TableRow>
                    {isOpen && (
                      <TableRow>
                        <TableCell colSpan={8} className="bg-secondary/30 p-0">
                          <div className="flex flex-wrap items-center justify-between gap-4 px-4 pt-3 text-xs text-muted-foreground">
                            <div className="flex flex-wrap gap-4">
                              <span>
                                Bulkhead:{" "}
                                <span className="font-mono text-foreground">
                                  {p.bulkhead_active}/{p.bulkhead_max}
                                </span>{" "}
                                in flight
                              </span>
                              <span>
                                Rate limit:{" "}
                                <span className="font-mono text-foreground">
                                  {p.rate_tokens.toFixed(1)}/{p.rate_burst.toFixed(0)}
                                </span>{" "}
                                tokens
                              </span>
                            </div>
                            <div className="flex items-center gap-2">
                              <Button
                                size="sm"
                                variant="outline"
                                disabled={busy.has(p.plugin_id)}
                                onClick={(e) => {
                                  e.stopPropagation();
                                  runAction(p.plugin_id, () => resetBreaker(p.plugin_id));
                                }}
                              >
                                <RotateCcw className="h-3.5 w-3.5" />
                                Reset breaker
                              </Button>
                              <Button
                                size="sm"
                                variant="destructive"
                                disabled={busy.has(p.plugin_id)}
                                onClick={(e) => {
                                  e.stopPropagation();
                                  if (
                                    !window.confirm(
                                      `Force-deregister "${p.plugin_name}"? Core will drop it from the registry — the plugin itself keeps running and can re-register on its next heartbeat.`,
                                    )
                                  )
                                    return;
                                  runAction(p.plugin_id, () => forceDeregister(p.plugin_id));
                                }}
                              >
                                <XCircle className="h-3.5 w-3.5" />
                                Force deregister
                              </Button>
                            </div>
                          </div>
                          {actionError[p.plugin_id] && (
                            <p className="px-4 pt-1 text-xs text-danger">
                              {actionError[p.plugin_id]}
                            </p>
                          )}
                          <div className="px-4 py-3">
                            {p.routes && p.routes.length > 0 ? (
                              <table className="w-full text-xs">
                                <thead>
                                  <tr className="text-muted-foreground">
                                    <th className="py-1 pr-4 text-left font-medium">Method</th>
                                    <th className="py-1 pr-4 text-left font-medium">Path</th>
                                    <th className="py-1 pr-4 text-left font-medium">Public</th>
                                    <th className="py-1 pr-4 text-left font-medium">Permission</th>
                                    <th className="py-1 text-left font-medium">Summary</th>
                                  </tr>
                                </thead>
                                <tbody>
                                  {p.routes.map((r, i) => (
                                    <tr key={i} className="border-t border-border/50">
                                      <td className="py-1 pr-4 font-mono">{r.method}</td>
                                      <td className="py-1 pr-4 font-mono">{r.path}</td>
                                      <td className="py-1 pr-4">{r.public ? "yes" : "no"}</td>
                                      <td className="py-1 pr-4">{r.permission || "—"}</td>
                                      <td className="py-1 text-muted-foreground">{r.summary || "—"}</td>
                                    </tr>
                                  ))}
                                </tbody>
                              </table>
                            ) : (
                              <p className="text-xs text-muted-foreground">No routes.</p>
                            )}
                          </div>
                        </TableCell>
                      </TableRow>
                    )}
                  </Fragment>
                );
              })}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}
