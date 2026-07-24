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
