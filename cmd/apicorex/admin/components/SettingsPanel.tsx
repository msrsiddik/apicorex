"use client";

import { useCallback, useEffect, useState } from "react";
import {
  fetchSettings,
  fetchSettingsHistory,
  queueCommand,
  restoreSetting,
  saveSettings,
  type PluginSettings,
  type SettingChange,
  type SettingView,
} from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/skeleton";
import { EmptyState } from "@/components/ui/empty-state";
import { relativeTime } from "@/lib/utils";
import { History, Power, SlidersHorizontal } from "lucide-react";

// What a plugin is actually running with for one setting, by the precedence
// the plugin applies: its environment, then this dashboard, then its default.
function source(s: SettingView): { label: string; variant: "warn" | "ok" | "default" } {
  if (s.from_env) return { label: "environment overrides", variant: "warn" };
  if (s.set) return { label: "dashboard", variant: "ok" };
  return { label: "default", variant: "default" };
}

export default function SettingsPanel() {
  const [data, setData] = useState<PluginSettings[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await fetchSettings());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-lg font-semibold">Settings</h1>
        <p className="text-sm text-muted-foreground">
          What each plugin lets you set here instead of in its environment. A variable set in the
          plugin's environment still wins — that is the way back if this screen is ever unavailable.
          Changes take effect when the plugin restarts. Secrets are sealed, never shown again, and reach a
          plugin only when it uses its own key (API keys).
        </p>
      </div>

      {error && (
        <div className="rounded-xl border bg-card px-4 py-3.5 text-sm text-danger">
          Failed to load settings: {error}
        </div>
      )}

      {!data ? (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-32 w-full" />
          <Skeleton className="h-32 w-full" />
        </div>
      ) : data.length === 0 ? (
        <EmptyState icon={SlidersHorizontal} title="No plugins registered" description="Plugins appear here as they register." />
      ) : (
        data.map((p) => <PluginCard key={p.plugin} p={p} onSaved={load} />)
      )}
    </div>
  );
}

function PluginCard({ p, onSaved }: { p: PluginSettings; onSaved: () => void }) {
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [showHistory, setShowHistory] = useState(false);

  const dirty = Object.keys(draft).length > 0;
  // A plugin built before settings cannot be behind: it never loads them.
  const behind = p.loads_settings && p.running_version !== p.version;

  async function save() {
    setBusy(true);
    setErr(null);
    try {
      const values: Record<string, string | null> = {};
      for (const [k, v] of Object.entries(draft)) values[k] = v === "" ? null : v;
      // A set-once value that is already set — an encryption or signing key —
      // is replaced only after saying what that breaks, by name.
      const confirm: string[] = [];
      for (const k of Object.keys(values)) {
        const s = p.settings.find((x) => x.key === k);
        if (!s?.set_once || !s.set) continue;
        const typed = window.prompt(
          `${k} is set-once. Replacing or clearing it breaks what was encrypted or signed with the current value.\n\n${s.description ?? ""}\n\nType ${k} to go ahead.`,
        );
        if (typed !== k) {
          setBusy(false);
          return;
        }
        confirm.push(k);
      }
      await saveSettings(p.plugin, values, note, confirm);
      setDraft({});
      setNote("");
      onSaved();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function restart() {
    if (!window.confirm(`Restart "${p.plugin}" so it loads its new settings? Callers get 503 "restarting" for a few seconds.`)) return;
    if (p.plugin === "identity" && !window.confirm("Identity handles every login. While it restarts, nobody can sign in.\n\nGo ahead?")) return;
    try {
      await queueCommand(p.plugin, "restart");
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <div className="rounded-xl border bg-card">
      <div className="flex flex-wrap items-start justify-between gap-3 p-5 pb-3">
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2">
            <span className="font-semibold">{p.plugin}</span>
            {!p.registered && <Badge variant="outline">not registered</Badge>}
          </div>
          {behind && (
            <span className="text-xs text-warn-foreground">
              Running settings v{p.running_version}, saved v{p.version} — restart to apply.
            </span>
          )}
          {p.registered && !p.loads_settings && (
            <span className="text-xs text-muted-foreground">This plugin's build does not report loading settings from Core yet.</span>
          )}
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="ghost" onClick={() => setShowHistory(!showHistory)}>
            <History /> History
          </Button>
          {behind && (
            <Button size="sm" variant="outline" onClick={restart}>
              <Power /> Restart
            </Button>
          )}
        </div>
      </div>

      {p.settings.length === 0 ? (
        <p className="px-5 pb-5 text-sm text-muted-foreground">
          {p.registered ? "This plugin declares no settings." : "Start the plugin to see its settings."}
        </p>
      ) : (
        <div className="px-5 pb-5">
          <table className="w-full text-sm">
            <thead>
              <tr className="text-left text-xs text-muted-foreground">
                <th className="w-1/3 py-1 pr-3 font-medium">Setting</th>
                <th className="py-1 pr-3 font-medium">Value</th>
                <th className="w-36 py-1 font-medium">In effect from</th>
              </tr>
            </thead>
            <tbody>
              {p.settings.map((s) => (
                <SettingRow
                  key={s.key}
                  s={s}
                  draft={draft[s.key]}
                  onChange={(v) => setDraft({ ...draft, [s.key]: v })}
                />
              ))}
            </tbody>
          </table>

          {p.undeclared.length > 0 && (
            <p className="mt-3 text-xs text-muted-foreground">
              Set here but no longer declared by the plugin: {p.undeclared.map((u) => u.key).join(", ")}.{" "}
              <button
                className="underline"
                onClick={() => setDraft({ ...draft, ...Object.fromEntries(p.undeclared.map((u) => [u.key, ""])) })}
              >
                Clear them
              </button>
            </p>
          )}

          {dirty && (
            <div className="mt-4 flex flex-wrap items-center gap-2">
              <Input
                className="min-w-[16rem] flex-1"
                placeholder="Note for the history (optional)"
                value={note}
                onChange={(e) => setNote(e.target.value)}
              />
              <Button size="sm" disabled={busy} onClick={save}>
                Save
              </Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => setDraft({})}>
                Discard
              </Button>
            </div>
          )}
          {err && <p className="mt-2 text-xs text-danger">{err}</p>}
        </div>
      )}

      {showHistory && (
        <div className="border-t p-5">
          <SettingsHistory plugin={p.plugin} onRestored={onSaved} />
        </div>
      )}
    </div>
  );
}

function SettingRow({ s, draft, onChange }: { s: SettingView; draft: string | undefined; onChange: (v: string) => void }) {
  const src = source(s);
  const value = draft ?? s.value;
  const placeholder = s.default ? `default: ${s.default}` : "not set";

  if (s.secret) {
    // The value is never sent back; the field only ever takes a new one.
    return (
      <tr className="border-t align-top">
        <td className="py-2 pr-3">
          <div className="flex items-center gap-1.5">
            <span className="font-mono text-xs">{s.key}</span>
            <Badge variant="outline">secret</Badge>
            {s.set_once && <Badge variant="warn">set-once</Badge>}
          </div>
          {s.description && <div className="text-xs text-muted-foreground">{s.description}</div>}
        </td>
        <td className="py-2 pr-3">
          <Input
            type="password"
            autoComplete="new-password"
            className="font-mono"
            placeholder={s.set ? "set — type to replace" : "not set"}
            value={draft ?? ""}
            onChange={(e) => onChange(e.target.value)}
          />
          {s.set && s.updated_at && (
            <div className="mt-1 text-xs text-muted-foreground">
              set {relativeTime(s.updated_at)} by {s.updated_by}
            </div>
          )}
        </td>
        <td className="py-2">
          <Badge variant={src.variant}>{src.label}</Badge>
        </td>
      </tr>
    );
  }

  return (
    <tr className="border-t align-top">
      <td className="py-2 pr-3">
        <div className="font-mono text-xs">{s.key}</div>
        {s.description && <div className="text-xs text-muted-foreground">{s.description}</div>}
      </td>
      <td className="py-2 pr-3">
        {s.type === "bool" ? (
          <select
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
            value={value}
            onChange={(e) => onChange(e.target.value)}
          >
            <option value="">{placeholder}</option>
            <option value="true">true</option>
            <option value="false">false</option>
          </select>
        ) : s.type === "enum" ? (
          <select
            className="h-9 w-full rounded-md border border-input bg-background px-2 text-sm"
            value={value}
            onChange={(e) => onChange(e.target.value)}
          >
            <option value="">{placeholder}</option>
            {(s.enum ?? []).map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </select>
        ) : (
          <Input
            className="font-mono"
            inputMode={s.type === "int" ? "numeric" : undefined}
            placeholder={placeholder}
            value={value}
            onChange={(e) => onChange(e.target.value)}
          />
        )}
        {s.set && s.updated_at && (
          <div className="mt-1 text-xs text-muted-foreground">
            set {relativeTime(s.updated_at)} by {s.updated_by}
          </div>
        )}
      </td>
      <td className="py-2">
        <Badge variant={src.variant}>{src.label}</Badge>
      </td>
    </tr>
  );
}

function SettingsHistory({ plugin, onRestored }: { plugin: string; onRestored: () => void }) {
  const [items, setItems] = useState<SettingChange[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const load = useCallback(() => {
    fetchSettingsHistory(plugin).then(setItems, (e) => setErr(e instanceof Error ? e.message : String(e)));
  }, [plugin]);
  useEffect(() => {
    load();
  }, [load]);

  async function restore(c: SettingChange) {
    const what = c.cleared ? "cleared" : c.secret ? "the secret value it had" : c.value;
    if (!window.confirm(`Make ${c.key} ${what} again (version ${c.version})? Saved as a new version; applies at the next restart.`)) return;
    try {
      await restoreSetting(plugin, c.version);
      load();
      onRestored();
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    }
  }
  if (err) return <p className="text-xs text-danger">{err}</p>;
  if (!items) return <Skeleton className="h-16 w-full" />;
  if (items.length === 0) return <p className="text-xs text-muted-foreground">No changes recorded yet.</p>;
  return (
    <table className="w-full text-xs">
      <tbody>
        {items.map((c) => (
          <tr key={c.version} className="border-t border-border/50">
            <td className="py-1 pr-3 font-mono">v{c.version}</td>
            <td className="py-1 pr-3 font-mono">{c.key}</td>
            <td className="py-1 pr-3 font-mono">
              {c.cleared ? (
                <span className="text-muted-foreground">cleared</span>
              ) : c.secret ? (
                <span className="text-muted-foreground">secret set</span>
              ) : (
                c.value
              )}
            </td>
            <td className="py-1 pr-3 text-muted-foreground">{c.note}</td>
            <td className="py-1 pr-3 text-right text-muted-foreground">
              {relativeTime(c.saved_at)} · {c.saved_by}
            </td>
            <td className="py-1 text-right">
              <button className="underline" onClick={() => restore(c)}>
                restore
              </button>
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
