// Client-side API helper. This UI is served by Core itself (same origin), so
// requests are plain relative fetches against Core's own JSON endpoints.
// /plugins and /metrics are unauthenticated Core endpoints (same as /health);
// the dashboard's own login/session gates access to the SPA and its write
// actions (reset-breaker, force-deregister) — see the login section below.

export async function api<T = any>(path: string): Promise<T> {
  const res = await fetch(path);
  let body: any = null;
  try {
    body = await res.json();
  } catch {
    /* no body */
  }
  if (!res.ok) {
    const msg = body && body.error ? body.error : `request failed (${res.status})`;
    throw new Error(msg);
  }
  return body as T;
}

export interface Route {
  method: string;
  path: string;
  public: boolean;
  permission?: string;
  summary?: string;
  tags?: string[];
}

export interface Plugin {
  plugin_id: string;
  plugin_name: string;
  version: string;
  base_url: string;
  status: "healthy" | "unhealthy";
  alive: boolean;
  registered_at: string;
  last_heartbeat: string;
  routes: Route[];
  circuit_state: "closed" | "half-open" | "open";
  bulkhead_active: number;
  bulkhead_max: number;
  rate_tokens: number;
  rate_burst: number;
}

export function fetchPlugins(): Promise<Plugin[]> {
  return api<Plugin[]>("/plugins");
}

// ── Dashboard login ──────────────────────────────────────────────────────────
// A single shared secret key (DASHBOARD_SECRET on Core), not a username/
// password pair. Logging in exchanges the key for a signed session token
// (12h TTL) that gates write actions. The key itself is never stored —
// only the session token, in localStorage.

const SESSION_STORAGE = "apicorex_dashboard_session";

export function sessionToken(): string {
  if (typeof window === "undefined") return "";
  return localStorage.getItem(SESSION_STORAGE) || "";
}

export function clearSession(): void {
  localStorage.removeItem(SESSION_STORAGE);
  // best-effort: also clears the session cookie server-side (used to gate
  // /docs), so logging out of the dashboard revokes docs access too
  fetch("/_core/admin/logout", { method: "POST" }).catch(() => {});
}

export async function loginRequired(): Promise<boolean> {
  const res = await api<{ required: boolean }>("/_core/admin/login-required");
  return res.required;
}

export async function login(key: string): Promise<void> {
  const res = await fetch("/_core/admin/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ key }),
  });
  const body = await res.json().catch(() => null);
  if (!res.ok) {
    throw new Error(body && body.error ? body.error : `request failed (${res.status})`);
  }
  localStorage.setItem(SESSION_STORAGE, body.token);
}

// checkSession validates the stored session token against Core, returning
// false (rather than throwing) for any failure — an expired/invalid/missing
// token all just mean "show the login form again".
export async function checkSession(): Promise<boolean> {
  const token = sessionToken();
  if (!token) return false;
  try {
    const res = await fetch("/_core/admin/session", {
      headers: { Authorization: `Bearer ${token}` },
    });
    return res.ok;
  } catch {
    return false;
  }
}

async function adminPost(path: string): Promise<void> {
  const res = await fetch(path, {
    method: "POST",
    headers: { Authorization: `Bearer ${sessionToken()}` },
  });
  let body: any = null;
  try {
    body = await res.json();
  } catch {
    /* no body */
  }
  if (!res.ok) {
    const msg = body && body.error ? body.error : `request failed (${res.status})`;
    throw new Error(msg);
  }
}

export function resetBreaker(pluginID: string): Promise<void> {
  return adminPost(`/_core/admin/plugins/${encodeURIComponent(pluginID)}/reset-breaker`);
}

export function forceDeregister(pluginID: string): Promise<void> {
  return adminPost(`/_core/admin/plugins/${encodeURIComponent(pluginID)}/deregister`);
}

// ── Config store: plugin database connections ──────────────────────────────
// Every route is behind the dashboard session. A stored DSN never comes back
// from Core — only dsn_display, with the password replaced.

async function adminJSON<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: {
      Authorization: `Bearer ${sessionToken()}`,
      ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  let out: any = null;
  try {
    out = await res.json();
  } catch {
    /* no body */
  }
  if (!res.ok) {
    throw new Error(out && out.error ? out.error : `request failed (${res.status})`);
  }
  return out as T;
}

// The default row every plugin inherits from.
export const DEFAULT_PLUGIN = "*";

// null = inherit (from the default, or the built-in when the default is unset too).
export interface PoolSettings {
  max_open: number | null;
  max_idle: number | null;
  conn_max_lifetime_s: number | null;
  conn_max_idle_s: number | null;
}

export interface EffectivePool {
  max_open: number;
  max_idle: number;
  conn_max_lifetime_s: number;
  conn_max_idle_s: number;
}

export interface DBConfigRow {
  plugin: string;
  has_dsn: boolean;
  dsn_display: string;
  pool: PoolSettings;
  version: number;
  updated_at: string;
  updated_by: string;
}

export interface DBPluginView {
  name: string;
  registered: boolean;
  has_own_row: boolean;
  dsn_source: "own" | "default" | "";
  effective: EffectivePool;
  version: number;
  // What the plugin itself last reported on its heartbeat.
  running: "core" | "env" | "";
  running_version: number;
}

export interface DBConfigOverview {
  has_master_key: boolean;
  writable: boolean;
  rows: DBConfigRow[];
  plugins: DBPluginView[];
  total_max_open: number;
}

export type DSNAction = "keep" | "set" | "inherit";

export interface DBConfigInput {
  dsn_action: DSNAction;
  dsn?: string;
  pool: PoolSettings;
  note?: string;
}

export interface DBConfigVersion {
  version: number;
  plugin: string;
  action: "save" | "delete" | "rollback";
  has_dsn: boolean;
  dsn_display: string;
  pool: PoolSettings;
  note: string;
  saved_at: string;
  saved_by: string;
}

export interface ProbeResult {
  current_user: string;
  server_version: string;
  max_connections: number;
  reserved_connections: number;
  in_use: number;
  latency_ms: number;
}

export interface ProbeResponse {
  ok: boolean;
  error?: string;
  result?: ProbeResult;
}

const enc = encodeURIComponent;

export function fetchDBConfig(): Promise<DBConfigOverview> {
  return adminJSON("GET", "/_core/admin/db-config");
}

export function saveDBConfig(plugin: string, input: DBConfigInput): Promise<DBConfigRow> {
  return adminJSON("PUT", `/_core/admin/db-config/${enc(plugin)}`, input);
}

export function deleteDBConfig(plugin: string): Promise<void> {
  return adminJSON("DELETE", `/_core/admin/db-config/${enc(plugin)}`);
}

export function fetchDBConfigHistory(plugin: string): Promise<DBConfigVersion[]> {
  return adminJSON("GET", `/_core/admin/db-config/${enc(plugin)}/history`);
}

export function rollbackDBConfig(plugin: string, version: number): Promise<DBConfigRow> {
  return adminJSON("POST", `/_core/admin/db-config/${enc(plugin)}/rollback`, { version });
}

export function testDBConfig(plugin: string, dsnAction: DSNAction, dsn?: string): Promise<ProbeResponse> {
  return adminJSON("POST", `/_core/admin/db-config/${enc(plugin)}/test`, { dsn_action: dsnAction, dsn });
}

export function fetchPostgres(): Promise<ProbeResponse> {
  return adminJSON("GET", "/_core/admin/postgres");
}

export interface AuditEntry {
  id: number;
  at: string;
  actor: string;
  action: string;
  target: string;
  detail: string;
}

export function fetchAudit(before?: number): Promise<AuditEntry[]> {
  const q = before ? `?limit=50&before=${before}` : "?limit=50";
  return adminJSON("GET", `/_core/admin/audit${q}`);
}

// ── Commands to a plugin ────────────────────────────────────────────────────
// Delivered on the plugin's next heartbeat (every ~15s), so a command shows
// as pending until then.

export type CommandKind = "restart" | "reload";

export interface PluginCommand {
  id: number;
  plugin: string;
  kind: CommandKind;
  state: "pending" | "delivered" | "done" | "failed" | "superseded" | "expired";
  result: string;
  requested_at: string;
  requested_by: string;
  delivered_at?: string;
  finished_at?: string;
}

export function queueCommand(plugin: string, kind: CommandKind): Promise<PluginCommand> {
  return adminJSON("POST", `/_core/admin/commands/${enc(plugin)}`, { kind });
}

export function fetchCommands(plugin: string): Promise<PluginCommand[]> {
  return adminJSON("GET", `/_core/admin/commands/${enc(plugin)}?limit=5`);
}

// ── Plugin settings ─────────────────────────────────────────────────────────
// Declared by each plugin in its manifest; Core renders and validates from
// the declaration and knows nothing else about them.

export interface SettingView {
  key: string;
  type: "string" | "int" | "bool" | "duration" | "time" | "url" | "enum" | "";
  default?: string;
  description?: string;
  enum?: string[];
  secret?: boolean;
  set_once?: boolean;
  value: string;
  set: boolean;
  updated_at?: string;
  updated_by?: string;
  from_env: boolean;
}

export interface PluginSettings {
  plugin: string;
  registered: boolean;
  settings: SettingView[];
  undeclared: { key: string; value: string }[];
  version: number;
  running_version: number;
  loads_settings: boolean;
}

export interface SettingChange {
  version: number;
  key: string;
  value: string | null;
  secret: boolean;
  cleared: boolean;
  note: string;
  saved_at: string;
  saved_by: string;
}

export function fetchSettings(): Promise<PluginSettings[]> {
  return adminJSON("GET", "/_core/admin/settings");
}

export function saveSettings(
  plugin: string,
  values: Record<string, string | null>,
  note: string,
  confirm: string[] = [],
): Promise<{ version: number }> {
  return adminJSON("PUT", `/_core/admin/settings/${enc(plugin)}`, { values, note, confirm });
}

export function restoreSetting(plugin: string, version: number): Promise<{ version: number }> {
  return adminJSON("POST", `/_core/admin/settings/${enc(plugin)}/restore`, { version });
}

export function fetchSettingsHistory(plugin: string): Promise<SettingChange[]> {
  return adminJSON("GET", `/_core/admin/settings/${enc(plugin)}/history`);
}

// ── API keys per plugin ─────────────────────────────────────────────────────

export interface PluginKey {
  id: number;
  plugin: string;
  hint: string;
  created_at: string;
  created_by: string;
  last_used_at?: string;
  revoked_at?: string;
  revoked_by?: string;
}

export interface PluginKeysView {
  plugin: string;
  registered: boolean;
  auth: "own" | "shared" | "";
  keys: PluginKey[];
}

export function fetchKeys(): Promise<{ accept_shared: boolean; plugins: PluginKeysView[] }> {
  return adminJSON("GET", "/_core/admin/keys");
}

export function issueKey(plugin: string): Promise<{ key: string; issued: PluginKey }> {
  return adminJSON("POST", `/_core/admin/keys/${enc(plugin)}`);
}

export function revokeKey(plugin: string, id: number): Promise<void> {
  return adminJSON("DELETE", `/_core/admin/keys/${enc(plugin)}/${id}`);
}

export function setSharedKey(accept: boolean, force = false): Promise<{ accept_shared: boolean }> {
  return adminJSON("PUT", "/_core/admin/shared-key", { accept, force });
}

// ── Store snapshots ─────────────────────────────────────────────────────────

export interface StoreSnapshot {
  name: string;
  size: number;
  created_at: string;
  uploaded: boolean;
  uploaded_at?: string;
}

export interface SnapshotStatus {
  last_snapshot?: string;
  last_error?: string;
  last_upload?: string;
  last_upload_error?: string;
  remote_configured: boolean;
  remote?: string;
  interval: string;
  keep: number;
}

export function fetchSnapshots(): Promise<{ status: SnapshotStatus; snapshots: StoreSnapshot[] }> {
  return adminJSON("GET", "/_core/admin/store/snapshots");
}

export function takeSnapshot(): Promise<StoreSnapshot> {
  return adminJSON("POST", "/_core/admin/store/snapshots");
}

// downloadSnapshot fetches with the session header — a plain link would carry
// none — and hands the browser the file.
export async function downloadSnapshot(name: string): Promise<void> {
  const res = await fetch(`/_core/admin/store/snapshots/${enc(name)}`, {
    headers: { Authorization: `Bearer ${sessionToken()}` },
  });
  if (!res.ok) {
    const body = await res.json().catch(() => null);
    throw new Error(body && body.error ? body.error : `request failed (${res.status})`);
  }
  const url = URL.createObjectURL(await res.blob());
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

// ── Prometheus text-exposition parsing ──────────────────────────────────────
// /metrics is plain-text (no JSON endpoint exists for it), so we parse just
// the handful of metric families the dashboard cares about. Lines look like:
//   metric_name{label="value",...} 123.0
// Comments (#) and unrecognized metric names are skipped.

export interface MetricSample {
  labels: Record<string, string>;
  value: number;
}

export type MetricFamilies = Record<string, MetricSample[]>;

const TRACKED_METRICS = [
  "apicorex_requests_total",
  "apicorex_requests_rejected_total",
  "apicorex_requests_in_flight",
  "apicorex_plugins_registered",
];

const LINE_RE = /^(\w+)(\{([^}]*)\})?\s+([0-9.eE+-]+|NaN|\+Inf|-Inf)$/;
const LABEL_RE = /(\w+)="((?:[^"\\]|\\.)*)"/g;

export async function fetchMetrics(): Promise<MetricFamilies> {
  const res = await fetch("/metrics");
  if (!res.ok) throw new Error(`request failed (${res.status})`);
  const text = await res.text();

  const out: MetricFamilies = {};
  for (const line of text.split("\n")) {
    if (!line || line.startsWith("#")) continue;
    const m = LINE_RE.exec(line.trim());
    if (!m) continue;
    const [, name, , labelStr, valueStr] = m;
    if (!TRACKED_METRICS.includes(name)) continue;

    const labels: Record<string, string> = {};
    if (labelStr) {
      let lm: RegExpExecArray | null;
      LABEL_RE.lastIndex = 0;
      while ((lm = LABEL_RE.exec(labelStr))) {
        labels[lm[1]] = lm[2];
      }
    }
    const value = parseFloat(valueStr);
    if (Number.isNaN(value)) continue;

    (out[name] ??= []).push({ labels, value });
  }
  return out;
}

export function sumMetric(families: MetricFamilies, name: string): number {
  return (families[name] ?? []).reduce((n, s) => n + s.value, 0);
}
