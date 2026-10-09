"use client";

import { Fragment, useCallback, useEffect, useState } from "react";
import {
  DEFAULT_PLUGIN,
  deleteDBConfig,
  fetchDBConfig,
  fetchDBConfigHistory,
  fetchPostgres,
  rollbackDBConfig,
  saveDBConfig,
  testDBConfig,
  type DBConfigOverview,
  type DBConfigRow,
  type DBConfigVersion,
  type DBPluginView,
  type DSNAction,
  type PoolSettings,
  type ProbeResponse,
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
import { relativeTime } from "@/lib/utils";
import { History, Pencil, PlugZap, RotateCcw, Trash2 } from "lucide-react";

// seconds renders a duration in seconds the way the pool settings are usually
// thought about: 1800 → "30m", 3600 → "1h".
function seconds(s: number): string {
  if (s === 0) return "0";
  if (s % 3600 === 0) return `${s / 3600}h`;
  if (s % 60 === 0) return `${s / 60}m`;
  return `${s}s`;
}

function poolSummary(p: PoolSettings): string {
  const parts: string[] = [];
  if (p.max_open !== null) parts.push(`open ${p.max_open}`);
  if (p.max_idle !== null) parts.push(`idle ${p.max_idle}`);
  if (p.conn_max_lifetime_s !== null) parts.push(`life ${seconds(p.conn_max_lifetime_s)}`);
  if (p.conn_max_idle_s !== null) parts.push(`idle-time ${seconds(p.conn_max_idle_s)}`);
  return parts.length ? parts.join(" · ") : "inherits pool";
}

function label(plugin: string): string {
  return plugin === DEFAULT_PLUGIN ? "Default (all plugins)" : plugin;
}

export default function DatabasePanel() {
  const [data, setData] = useState<DBConfigOverview | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [pg, setPg] = useState<ProbeResponse | null>(null);
  const [editing, setEditing] = useState<string | null>(null);
  const [historyOf, setHistoryOf] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await fetchDBConfig());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  // Postgres is asked separately and only on demand: it opens a connection,
  // which is not something to do on every poll.
  const checkPostgres = useCallback(async () => {
    setPg(null);
    try {
      setPg(await fetchPostgres());
    } catch (e) {
      setPg({ ok: false, error: e instanceof Error ? e.message : String(e) });
    }
  }, []);

  useEffect(() => {
    load();
    checkPostgres();
  }, [load, checkPostgres]);

  const rowFor = (plugin: string) => data?.rows.find((r) => r.plugin === plugin);
  const defaultRow = rowFor(DEFAULT_PLUGIN);

  async function removeOverride(plugin: string) {
    if (!window.confirm(`Remove "${plugin}"'s own settings? It will use the default for everything.`)) return;
    try {
      await deleteDBConfig(plugin);
      await load();
    } catch (e) {
      window.alert(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-lg font-semibold">Databases</h1>
        <p className="text-sm text-muted-foreground">
          Where each plugin connects, and how big its pool is. A plugin uses the default unless it has
          settings of its own, field by field.
        </p>
      </div>

      {error && (
        <div className="rounded-xl border bg-card px-4 py-3.5 text-sm text-danger">
          Failed to load database config: {error}
        </div>
      )}

      {data && !data.has_master_key && (
        <div className="rounded-xl border border-warn/40 bg-warn-surface px-4 py-3.5 text-sm text-warn-foreground">
          CORE_MASTER_KEY is not set, so connection strings cannot be saved or read. Pool sizes still
          work. Set the key and restart Core.
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
          <Budget total={data.total_max_open} pg={pg} onCheck={checkPostgres} />

          <div className="rounded-xl border bg-card">
            <div className="flex flex-wrap items-start justify-between gap-3 p-5">
              <div className="flex flex-col gap-1">
                <span className="font-semibold">Default</span>
                <span className="font-mono text-xs text-muted-foreground">
                  {defaultRow?.has_dsn ? defaultRow.dsn_display : "no connection string set"}
                </span>
                <span className="text-xs text-muted-foreground">
                  {defaultRow ? poolSummary(defaultRow.pool) : "built-in pool: open 10 · idle 2 · life 30m · idle-time 5m"}
                </span>
              </div>
              <div className="flex gap-2">
                <Button size="sm" variant="outline" onClick={() => setHistoryOf(historyOf === DEFAULT_PLUGIN ? null : DEFAULT_PLUGIN)}>
                  <History /> History
                </Button>
                <Button size="sm" disabled={!data.writable} onClick={() => setEditing(editing === DEFAULT_PLUGIN ? null : DEFAULT_PLUGIN)}>
                  <Pencil /> Edit
                </Button>
              </div>
            </div>
            {editing === DEFAULT_PLUGIN && (
              <div className="border-t p-5">
                <Editor
                  plugin={DEFAULT_PLUGIN}
                  row={defaultRow}
                  canSeal={data.has_master_key}
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
                    <TableHead>Connection</TableHead>
                    <TableHead>Pool (effective)</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {data.plugins.map((p) => {
                    const own = rowFor(p.name);
                    return (
                      <Fragment key={p.name}>
                        <TableRow>
                          <TableCell>
                            <div className="flex items-center gap-2">
                              <span className="font-medium">{p.name}</span>
                              {!p.registered && <Badge variant="outline">not registered</Badge>}
                            </div>
                            <Running p={p} />
                          </TableCell>
                          <TableCell>
                            {p.dsn_source === "own" ? (
                              <span className="font-mono text-xs">{own?.dsn_display}</span>
                            ) : p.dsn_source === "default" ? (
                              <Badge>uses default</Badge>
                            ) : (
                              <Badge variant="warn">none set</Badge>
                            )}
                          </TableCell>
                          <TableCell className="text-xs text-muted-foreground">
                            <span className="font-mono text-foreground">
                              {p.effective.max_open}/{p.effective.max_idle}
                            </span>{" "}
                            open/idle · life {seconds(p.effective.conn_max_lifetime_s)} · idle-time{" "}
                            {seconds(p.effective.conn_max_idle_s)}
                            {own && <div>own: {poolSummary(own.pool)}</div>}
                          </TableCell>
                          <TableCell>
                            <div className="flex justify-end gap-2">
                              <Button size="sm" variant="ghost" title="History" onClick={() => setHistoryOf(historyOf === p.name ? null : p.name)}>
                                <History />
                              </Button>
                              {own && (
                                <Button size="sm" variant="ghost" title="Use the default for everything" disabled={!data.writable} onClick={() => removeOverride(p.name)}>
                                  <Trash2 />
                                </Button>
                              )}
                              <Button size="sm" variant="outline" disabled={!data.writable} onClick={() => setEditing(editing === p.name ? null : p.name)}>
                                <Pencil /> Edit
                              </Button>
                            </div>
                          </TableCell>
                        </TableRow>
                        {editing === p.name && (
                          <TableRow>
                            <TableCell colSpan={4} className="bg-secondary/30 p-5">
                              <Editor
                                plugin={p.name}
                                row={own}
                                canSeal={data.has_master_key}
                                onDone={() => {
                                  setEditing(null);
                                  load();
                                }}
                              />
                            </TableCell>
                          </TableRow>
                        )}
                        {historyOf === p.name && (
                          <TableRow>
                            <TableCell colSpan={4} className="bg-secondary/30 p-5">
                              <HistoryList plugin={p.name} writable={data.writable} onRolledBack={load} />
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
        </>
      )}
    </div>
  );
}

// Running says whether a saved change has reached the plugin, from what the
// plugin reports on its heartbeat.
function Running({ p }: { p: DBPluginView }) {
  if (!p.registered) return null;
  if (p.running === "env") {
    return (
      <div className="mt-1 text-xs text-warn-foreground">
        uses its own DATABASE_URL — changes here do not reach it
      </div>
    );
  }
  if (p.running === "core") {
    return p.running_version === p.version ? (
      <div className="mt-1 text-xs text-ok-foreground">running the latest (v{p.running_version})</div>
    ) : (
      <div className="mt-1 text-xs text-warn-foreground">
        running v{p.running_version}, latest is v{p.version} — picks it up on its next heartbeat, or, if
        it cannot swap its pool in place, when restarted from Plugins &amp; routes
      </div>
    );
  }
  return <div className="mt-1 text-xs text-muted-foreground">does not report its database</div>;
}

function Budget({
  total,
  pg,
  onCheck,
}: {
  total: number;
  pg: ProbeResponse | null;
  onCheck: () => void;
}) {
  const r = pg?.ok ? pg.result : undefined;
  const usable = r ? r.max_connections - r.reserved_connections : 0;
  const over = r ? total > usable : false;
  return (
    <div className="rounded-xl border bg-card p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex flex-col gap-1">
          <span className="font-semibold">Connection budget</span>
          <span className="text-sm text-muted-foreground">
            Every plugin's max open connections together, against what Postgres allows.
          </span>
        </div>
        <Button size="sm" variant="outline" onClick={onCheck}>
          <RotateCcw /> Check Postgres
        </Button>
      </div>
      <div className="mt-4 flex flex-wrap gap-8 text-sm">
        <div>
          <div className="text-xs text-muted-foreground">Plugins may open</div>
          <div className={over ? "text-xl font-semibold text-danger" : "text-xl font-semibold"}>{total}</div>
        </div>
        {r && (
          <>
            <div>
              <div className="text-xs text-muted-foreground">Postgres allows</div>
              <div className="text-xl font-semibold">{usable}</div>
              <div className="text-xs text-muted-foreground">
                {r.max_connections} max, {r.reserved_connections} reserved
              </div>
            </div>
            <div>
              <div className="text-xs text-muted-foreground">In use now</div>
              <div className="text-xl font-semibold">{r.in_use}</div>
            </div>
            <div>
              <div className="text-xs text-muted-foreground">Server</div>
              <div className="font-mono text-xs">{r.server_version}</div>
              <div className="text-xs text-muted-foreground">as {r.current_user}</div>
            </div>
          </>
        )}
      </div>
      {pg === null && <p className="mt-3 text-xs text-muted-foreground">Asking Postgres…</p>}
      {pg && !pg.ok && (
        <p className="mt-3 text-xs text-muted-foreground">
          Could not read Postgres limits through the default connection: {pg.error}
        </p>
      )}
      {over && (
        <p className="mt-3 text-xs text-danger">
          Over budget: under full load the plugins together would ask for more connections than Postgres
          accepts, and the last to ask would fail.
        </p>
      )}
    </div>
  );
}

const POOL_FIELDS: { key: keyof PoolSettings; label: string; hint: string }[] = [
  { key: "max_open", label: "Max open", hint: "connections" },
  { key: "max_idle", label: "Max idle", hint: "connections" },
  { key: "conn_max_lifetime_s", label: "Max lifetime", hint: "seconds" },
  { key: "conn_max_idle_s", label: "Max idle time", hint: "seconds" },
];

function Editor({
  plugin,
  row,
  canSeal,
  onDone,
}: {
  plugin: string;
  row: DBConfigRow | undefined;
  canSeal: boolean;
  onDone: () => void;
}) {
  const isDefault = plugin === DEFAULT_PLUGIN;
  // The stored DSN is never sent to the browser, so the form cannot show it
  // for editing. "keep" leaves it as it is; typing a new one replaces it.
  const [action, setAction] = useState<DSNAction>(row?.has_dsn ? "keep" : isDefault ? "set" : "inherit");
  const [dsn, setDsn] = useState("");
  const [pool, setPool] = useState<Record<keyof PoolSettings, string>>({
    max_open: row?.pool.max_open?.toString() ?? "",
    max_idle: row?.pool.max_idle?.toString() ?? "",
    conn_max_lifetime_s: row?.pool.conn_max_lifetime_s?.toString() ?? "",
    conn_max_idle_s: row?.pool.conn_max_idle_s?.toString() ?? "",
  });
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [probe, setProbe] = useState<ProbeResponse | null>(null);

  function parsedPool(): PoolSettings | string {
    const out: PoolSettings = { max_open: null, max_idle: null, conn_max_lifetime_s: null, conn_max_idle_s: null };
    for (const f of POOL_FIELDS) {
      const v = pool[f.key].trim();
      if (v === "") continue;
      const n = Number(v);
      if (!Number.isInteger(n) || n < 0) return `${f.label} must be a whole number`;
      out[f.key] = n;
    }
    return out;
  }

  async function test() {
    setProbe(null);
    setErr(null);
    setBusy(true);
    try {
      setProbe(await testDBConfig(plugin, action === "set" ? "set" : "keep", action === "set" ? dsn : undefined));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function save() {
    const p = parsedPool();
    if (typeof p === "string") {
      setErr(p);
      return;
    }
    if (action === "set" && !dsn.trim()) {
      setErr("Enter a connection string, or choose to keep the current one.");
      return;
    }
    setErr(null);
    setBusy(true);
    try {
      await saveDBConfig(plugin, { dsn_action: action, dsn: action === "set" ? dsn : undefined, pool: p, note });
      onDone();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const choices: { value: DSNAction; text: string }[] = [
    ...(row?.has_dsn ? [{ value: "keep" as DSNAction, text: `Keep current (${row.dsn_display})` }] : []),
    { value: "set", text: "Set a new connection string" },
    ...(!isDefault ? [{ value: "inherit" as DSNAction, text: "Use the default's" }] : []),
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="text-sm font-semibold">Edit {label(plugin)}</div>

      <fieldset className="flex flex-col gap-2">
        <legend className="mb-1 text-xs font-medium text-muted-foreground">Connection string</legend>
        {choices.map((c) => (
          <label key={c.value} className="flex items-center gap-2 text-sm">
            <input type="radio" name={`dsn-${plugin}`} checked={action === c.value} onChange={() => setAction(c.value)} />
            <span className={c.value === "keep" ? "font-mono text-xs" : ""}>{c.text}</span>
          </label>
        ))}
        {action === "set" && (
          <Input
            type="password"
            autoComplete="off"
            spellCheck={false}
            placeholder="postgres://user:password@host:5432/dbname?sslmode=disable"
            value={dsn}
            onChange={(e) => setDsn(e.target.value)}
            className="font-mono"
          />
        )}
        {action === "set" && !canSeal && (
          <p className="text-xs text-warn-foreground">CORE_MASTER_KEY is not set, so this cannot be saved.</p>
        )}
      </fieldset>

      <fieldset>
        <legend className="mb-2 text-xs font-medium text-muted-foreground">
          Pool — leave a field blank to {isDefault ? "use the built-in value" : "use the default's"}
        </legend>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          {POOL_FIELDS.map((f) => (
            <label key={f.key} className="flex flex-col gap-1 text-xs">
              <span>
                {f.label} <span className="text-muted-foreground">({f.hint})</span>
              </span>
              <Input
                inputMode="numeric"
                value={pool[f.key]}
                onChange={(e) => setPool({ ...pool, [f.key]: e.target.value })}
              />
            </label>
          ))}
        </div>
      </fieldset>

      <label className="flex flex-col gap-1 text-xs">
        <span>Note for the history (optional)</span>
        <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="why this changed" />
      </label>

      {probe && (
        <div className={probe.ok ? "text-xs text-ok-foreground" : "text-xs text-danger"}>
          {probe.ok && probe.result
            ? `Connected as ${probe.result.current_user} in ${probe.result.latency_ms} ms (Postgres ${probe.result.server_version}). Tested from Core — the plugin checks again from its own network when it loads this.`
            : `Connection failed: ${probe.error}`}
        </div>
      )}
      {err && <div className="text-xs text-danger">{err}</div>}

      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="outline" disabled={busy || action === "inherit"} onClick={test}>
          <PlugZap /> Test connection
        </Button>
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
  const [items, setItems] = useState<DBConfigVersion[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setItems(await fetchDBConfigHistory(plugin));
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }, [plugin]);

  useEffect(() => {
    load();
  }, [load]);

  async function rollback(v: DBConfigVersion) {
    if (!window.confirm(`Make version ${v.version} of ${label(plugin)} current again? This is saved as a new version.`)) return;
    try {
      await rollbackDBConfig(plugin, v.version);
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
            <th className="py-1 pr-4 text-left font-medium">Connection</th>
            <th className="py-1 pr-4 text-left font-medium">Pool</th>
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
              <td className="py-1.5 pr-4 font-mono">{v.action === "delete" ? "—" : v.has_dsn ? v.dsn_display : "inherited"}</td>
              <td className="py-1.5 pr-4">{v.action === "delete" ? "—" : poolSummary(v.pool)}</td>
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

