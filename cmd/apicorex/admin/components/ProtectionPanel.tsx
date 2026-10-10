"use client";

import { Fragment, useCallback, useEffect, useState } from "react";
import {
  DEFAULT_PLUGIN,
  deleteProtection,
  fetchProtection,
  fetchProtectionHistory,
  rollbackProtection,
  saveProtection,
  type ProtectionFields,
  type ProtectionKey,
  type ProtectionOverview,
  type ProtectionRow,
  type ProtectionVersion,
  type ProtectionView,
  type ResolvedLimits,
} from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Skeleton } from "@/components/ui/skeleton";
import { cn, relativeTime } from "@/lib/utils";
import { History, Pencil, RotateCcw, Trash2 } from "lucide-react";

// duration renders milliseconds the way limits are thought about: 120000 →
// "2m", 30000 → "30s", 1500 → "1.5s".
function duration(ms: number): string {
  if (ms % 60_000 === 0 && ms >= 60_000) return `${ms / 60_000}m`;
  return `${ms / 1000}s`;
}

function label(plugin: string): string {
  return plugin === DEFAULT_PLUGIN ? "Default (all plugins)" : plugin;
}

function sourceText(source: string): string {
  if (source === DEFAULT_PLUGIN) return "from the default";
  if (source === "config") return "from Core's environment or CONFIG_FILE";
  return "set for this plugin";
}

// Val shows one resolved value, styled by where it comes from: a plugin's
// own value stands out, an inherited one recedes.
function Val({ source, plugin, children }: { source: string; plugin: string; children: React.ReactNode }) {
  const own = source === plugin && plugin !== DEFAULT_PLUGIN;
  return (
    <span
      title={sourceText(source)}
      className={cn(
        own ? "font-semibold text-foreground" : source === DEFAULT_PLUGIN ? "text-foreground" : "text-muted-foreground",
      )}
    >
      {children}
    </span>
  );
}

function fieldsSummary(f: ProtectionFields): string {
  const parts: string[] = [];
  if (f.rate_per_sec !== null) parts.push(`rate ${f.rate_per_sec}/s`);
  if (f.rate_burst !== null) parts.push(`burst ${f.rate_burst}`);
  if (f.tenant_rate_per_sec !== null) parts.push(f.tenant_rate_per_sec === 0 ? "per tenant off" : `per tenant ${f.tenant_rate_per_sec}/s`);
  if (f.tenant_rate_burst !== null) parts.push(`tenant burst ${f.tenant_rate_burst}`);
  if (f.bulkhead_max !== null) parts.push(`bulkhead ${f.bulkhead_max}`);
  if (f.cb_threshold !== null) parts.push(`breaker at ${f.cb_threshold}`);
  if (f.cb_reset_timeout_ms !== null) parts.push(`reset ${duration(f.cb_reset_timeout_ms)}`);
  if (f.request_timeout_ms !== null) parts.push(`timeout ${duration(f.request_timeout_ms)}`);
  return parts.length ? parts.join(" · ") : "everything inherited";
}

export default function ProtectionPanel() {
  const [data, setData] = useState<ProtectionOverview | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const [historyOf, setHistoryOf] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await fetchProtection());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const rowFor = (plugin: string) => data?.rows.find((r) => r.plugin === plugin);

  async function removeOverride(plugin: string) {
    const what = plugin === DEFAULT_PLUGIN ? "the default's limits? Every plugin falls back to Core's environment for anything it does not set itself." : `"${plugin}"'s own limits? It will inherit everything.`;
    if (!window.confirm(`Remove ${what}`)) return;
    try {
      await deleteProtection(plugin);
      await load();
    } catch (e) {
      window.alert(e instanceof Error ? e.message : String(e));
    }
  }

  const toggle = (set: (v: string | null) => void, cur: string | null, plugin: string) => set(cur === plugin ? null : plugin);

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-lg font-semibold">Protection</h1>
        <p className="text-sm text-muted-foreground">
          How much traffic each plugin takes, how many requests it may have open at once, when its circuit
          breaker opens, and how long it may take to start answering. A change applies to the next request —
          no restart. Requests already running keep their place.
        </p>
      </div>

      {error && (
        <div className="rounded-xl border bg-card px-4 py-3.5 text-sm text-danger">
          Failed to load protection limits: {error}
        </div>
      )}
      {data && !data.writable && (
        <div className="rounded-xl border border-warn/40 bg-warn-surface px-4 py-3.5 text-sm text-warn-foreground">
          The dashboard login is off (APICOREX_SECRET is not set), so this page is read-only.
        </div>
      )}

      {!data ? (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-11 w-full" />
          <Skeleton className="h-11 w-full" />
        </div>
      ) : (
        <>
          <div className="rounded-xl border bg-card">
            <div className="flex flex-wrap items-start justify-between gap-3 p-5">
              <div className="flex flex-col gap-1">
                <span className="font-semibold">Default</span>
                <span className="text-xs text-muted-foreground">
                  Every plugin inherits these, field by field, unless it has its own. A field not set here
                  follows Core&apos;s environment (REQUEST_TIMEOUT, BULKHEAD_MAX, …).
                </span>
                <Summary view={data.default} />
              </div>
              <div className="flex gap-2">
                <Button size="sm" variant="outline" onClick={() => toggle(setHistoryOf, historyOf, DEFAULT_PLUGIN)}>
                  <History /> History
                </Button>
                {rowFor(DEFAULT_PLUGIN) && (
                  <Button size="sm" variant="ghost" title="Clear the default" disabled={!data.writable} onClick={() => removeOverride(DEFAULT_PLUGIN)}>
                    <Trash2 />
                  </Button>
                )}
                <Button size="sm" disabled={!data.writable} onClick={() => toggle(setEditing, editing, DEFAULT_PLUGIN)}>
                  <Pencil /> Edit
                </Button>
              </div>
            </div>
            {editing === DEFAULT_PLUGIN && (
              <div className="border-t p-5">
                <Editor
                  plugin={DEFAULT_PLUGIN}
                  row={rowFor(DEFAULT_PLUGIN)}
                  inherited={data.config}
                  onDone={() => {
                    setEditing(null);
                    load();
                  }}
                />
              </div>
            )}
            {historyOf === DEFAULT_PLUGIN && (
              <div className="border-t p-5">
                <HistoryList plugin={DEFAULT_PLUGIN} writable={data.writable} onRolledBack={load} />
              </div>
            )}
          </div>

          {data.plugins.length === 0 ? (
            <p className="text-sm text-muted-foreground">
              No plugins registered and none configured yet. Plugins appear here as they register.
            </p>
          ) : (
            <div className="rounded-xl border bg-card">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Plugin</TableHead>
                    <TableHead>Rate</TableHead>
                    <TableHead>Per tenant</TableHead>
                    <TableHead>Bulkhead</TableHead>
                    <TableHead>Breaker</TableHead>
                    <TableHead>Timeout</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.plugins.map((p) => {
                    const own = rowFor(p.plugin);
                    const e = p.effective;
                    const s = p.source;
                    return (
                      <Fragment key={p.plugin}>
                        <TableRow>
                          <TableCell>
                            <div className="flex items-center gap-2">
                              <span className="font-medium">{p.plugin}</span>
                              {!p.registered && <Badge variant="outline">not registered</Badge>}
                            </div>
                            <Running view={p} />
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            <Val source={s.rate_per_sec} plugin={p.plugin}>{e.rate_per_sec}/s</Val>
                            <div>
                              burst <Val source={s.rate_burst} plugin={p.plugin}>{e.rate_burst}</Val>
                            </div>
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            {e.tenant_rate_per_sec === 0 ? (
                              <Val source={s.tenant_rate_per_sec} plugin={p.plugin}>off</Val>
                            ) : (
                              <>
                                <Val source={s.tenant_rate_per_sec} plugin={p.plugin}>{e.tenant_rate_per_sec}/s</Val>
                                <div>
                                  burst <Val source={s.tenant_rate_burst} plugin={p.plugin}>{e.tenant_rate_burst}</Val>
                                </div>
                              </>
                            )}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            <Val source={s.bulkhead_max} plugin={p.plugin}>{e.bulkhead_max}</Val> at once
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            opens at <Val source={s.cb_threshold} plugin={p.plugin}>{e.cb_threshold}</Val> failures
                            <div>
                              retries after <Val source={s.cb_reset_timeout_ms} plugin={p.plugin}>{duration(e.cb_reset_timeout_ms)}</Val>
                            </div>
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            <Val source={s.request_timeout_ms} plugin={p.plugin}>{duration(e.request_timeout_ms)}</Val>
                          </TableCell>
                          <TableCell>
                            <div className="flex justify-end gap-2">
                              <Button size="sm" variant="ghost" title="History" onClick={() => toggle(setHistoryOf, historyOf, p.plugin)}>
                                <History />
                              </Button>
                              {own && (
                                <Button size="sm" variant="ghost" title="Inherit everything" disabled={!data.writable} onClick={() => removeOverride(p.plugin)}>
                                  <Trash2 />
                                </Button>
                              )}
                              <Button size="sm" variant="outline" disabled={!data.writable} onClick={() => toggle(setEditing, editing, p.plugin)}>
                                <Pencil /> Edit
                              </Button>
                            </div>
                          </TableCell>
                        </TableRow>
                        {editing === p.plugin && (
                          <TableRow>
                            <TableCell colSpan={7} className="bg-secondary/30 p-5">
                              <Editor
                                plugin={p.plugin}
                                row={own}
                                inherited={data.default.effective}
                                onDone={() => {
                                  setEditing(null);
                                  load();
                                }}
                              />
                            </TableCell>
                          </TableRow>
                        )}
                        {historyOf === p.plugin && (
                          <TableRow>
                            <TableCell colSpan={7} className="bg-secondary/30 p-5">
                              <HistoryList plugin={p.plugin} writable={data.writable} onRolledBack={load} />
                            </TableCell>
                          </TableRow>
                        )}
                      </Fragment>
                    );
                  })}
                </TableBody>
              </Table>
              <p className="border-t px-4 py-2.5 text-xs text-muted-foreground">
                Values in <span className="font-semibold text-foreground">bold</span> are set for that plugin,{" "}
                <span className="text-foreground">white</span>{" "}
                come from the default, grey from Core&apos;s
                environment. Hover a value to see where it comes from.
              </p>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function Summary({ view }: { view: ProtectionView }) {
  const e = view.effective;
  const s = view.source;
  const p = view.plugin;
  return (
    <span className="text-xs text-muted-foreground">
      <Val source={s.rate_per_sec} plugin={p}>{e.rate_per_sec}/s</Val> (burst{" "}
      <Val source={s.rate_burst} plugin={p}>{e.rate_burst}</Val>) · per tenant{" "}
      <Val source={s.tenant_rate_per_sec} plugin={p}>
        {e.tenant_rate_per_sec === 0 ? "off" : `${e.tenant_rate_per_sec}/s`}
      </Val>{" "}
      · bulkhead <Val source={s.bulkhead_max} plugin={p}>{e.bulkhead_max}</Val> · breaker at{" "}
      <Val source={s.cb_threshold} plugin={p}>{e.cb_threshold}</Val>, retries after{" "}
      <Val source={s.cb_reset_timeout_ms} plugin={p}>{duration(e.cb_reset_timeout_ms)}</Val> · timeout{" "}
      <Val source={s.request_timeout_ms} plugin={p}>{duration(e.request_timeout_ms)}</Val>
    </span>
  );
}

// Running says how what the plugin runs differs from what it resolves to.
// The one expected difference is a public plugin's rate: without one of its
// own it runs at a tenth of the inherited rate.
function Running({ view }: { view: ProtectionView }) {
  if (!view.running) return null;
  const diff = (Object.keys(view.effective) as ProtectionKey[]).filter((k) => view.running![k] !== view.effective[k]);
  if (diff.length === 0) return null;
  const rateOnly = diff.every((k) => k.includes("rate"));
  return (
    <div className="mt-1 text-xs text-muted-foreground">
      {rateOnly
        ? `public plugin: runs at ${view.running.rate_per_sec}/s, a tenth of the inherited rate — give it a rate of its own to change that`
        : `runs ${diff.join(", ")} differently — reload this page`}
    </div>
  );
}

type FieldDef = { key: ProtectionKey; label: string; unit: string; whole?: boolean; seconds?: boolean };

const FIELDS: FieldDef[] = [
  { key: "rate_per_sec", label: "Rate", unit: "requests/s" },
  { key: "rate_burst", label: "Burst", unit: "requests" },
  { key: "tenant_rate_per_sec", label: "Per-tenant rate", unit: "requests/s, 0 = off" },
  { key: "tenant_rate_burst", label: "Per-tenant burst", unit: "requests" },
  { key: "bulkhead_max", label: "Bulkhead", unit: "at once", whole: true },
  { key: "cb_threshold", label: "Breaker opens at", unit: "failures", whole: true },
  { key: "cb_reset_timeout_ms", label: "Breaker retries after", unit: "seconds", seconds: true },
  { key: "request_timeout_ms", label: "Request timeout", unit: "seconds", seconds: true },
];

function shown(f: FieldDef, v: number): string {
  return f.seconds ? String(v / 1000) : String(v);
}

function Editor({
  plugin,
  row,
  inherited,
  onDone,
}: {
  plugin: string;
  row: ProtectionRow | undefined;
  // What each field falls back to when left blank, for the placeholders.
  inherited: ResolvedLimits;
  onDone: () => void;
}) {
  const isDefault = plugin === DEFAULT_PLUGIN;
  const [values, setValues] = useState<Record<ProtectionKey, string>>(() => {
    const out = {} as Record<ProtectionKey, string>;
    for (const f of FIELDS) {
      const v = row?.limits[f.key];
      out[f.key] = v === null || v === undefined ? "" : shown(f, v);
    }
    return out;
  });
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  function parsed(): ProtectionFields | string {
    const out = {} as ProtectionFields;
    for (const f of FIELDS) {
      const raw = values[f.key].trim();
      if (raw === "") {
        out[f.key] = null;
        continue;
      }
      const n = Number(raw);
      if (!Number.isFinite(n) || n < 0) return `${f.label} must be a number`;
      if (f.whole && !Number.isInteger(n)) return `${f.label} must be a whole number`;
      out[f.key] = f.seconds ? Math.round(n * 1000) : n;
    }
    return out;
  }

  async function save() {
    const p = parsed();
    if (typeof p === "string") {
      setErr(p);
      return;
    }
    setErr(null);
    setBusy(true);
    try {
      await saveProtection(plugin, p, note);
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="text-sm font-semibold">Edit {label(plugin)}</div>
      <fieldset>
        <legend className="mb-2 text-xs font-medium text-muted-foreground">
          Leave a field blank to {isDefault ? "follow Core's environment" : "use the default's"} — shown greyed in
          the field
        </legend>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {FIELDS.map((f) => (
            <label key={f.key} className="flex flex-col gap-1 text-xs">
              <span>
                {f.label} <span className="text-muted-foreground">({f.unit})</span>
              </span>
              <Input
                inputMode="decimal"
                value={values[f.key]}
                placeholder={shown(f, inherited[f.key])}
                onChange={(e) => setValues({ ...values, [f.key]: e.target.value })}
              />
            </label>
          ))}
        </div>
      </fieldset>
      <label className="flex flex-col gap-1 text-xs">
        <span>Note for the history (optional)</span>
        <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="why this changed" />
      </label>
      {err && <div className="text-xs text-danger">{err}</div>}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" disabled={busy} onClick={save}>
          Save
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={onDone}>
          Cancel
        </Button>
      </div>
    </div>
  );
}

function HistoryList({
  plugin,
  writable,
  onRolledBack,
}: {
  plugin: string;
  writable: boolean;
  onRolledBack: () => void;
}) {
  const [items, setItems] = useState<ProtectionVersion[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setItems(await fetchProtectionHistory(plugin));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }, [plugin]);

  useEffect(() => {
    load();
  }, [load]);

  async function rollback(v: ProtectionVersion) {
    if (!window.confirm(`Make version ${v.version} of ${label(plugin)} current again? This is saved as a new version and applies at once.`)) return;
    try {
      await rollbackProtection(plugin, v.version);
      await load();
      onRolledBack();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  if (err) return <p className="text-xs text-danger">{err}</p>;
  if (!items) return <Skeleton className="h-16 w-full" />;
  if (items.length === 0) return <p className="text-xs text-muted-foreground">No changes recorded yet.</p>;

  return (
    <div className="flex flex-col gap-2">
      <div className="text-sm font-semibold">History of {label(plugin)}</div>
      <table className="w-full text-xs">
        <thead>
          <tr className="text-muted-foreground">
            <th className="py-1 pr-4 text-left font-medium">Version</th>
            <th className="py-1 pr-4 text-left font-medium">Change</th>
            <th className="py-1 pr-4 text-left font-medium">Limits</th>
            <th className="py-1 pr-4 text-left font-medium">When / who</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {items.map((v, i) => (
            <tr key={v.version} className="border-t border-border/50 align-top">
              <td className="py-1.5 pr-4 font-mono">
                {v.version}
                {i === 0 && <Badge variant="ok" className="ml-2">current</Badge>}
              </td>
              <td className="py-1.5 pr-4">
                {v.action}
                {v.note && <div className="text-muted-foreground">{v.note}</div>}
              </td>
              <td className="py-1.5 pr-4">{v.action === "delete" ? "— (inherits)" : fieldsSummary(v.limits)}</td>
              <td className="py-1.5 pr-4 text-muted-foreground">
                {relativeTime(v.saved_at)}
                <div>{v.saved_by}</div>
              </td>
              <td className="py-1.5 text-right">
                {i > 0 && (
                  <Button size="sm" variant="outline" disabled={!writable} onClick={() => rollback(v)}>
                    <RotateCcw /> Roll back
                  </Button>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
