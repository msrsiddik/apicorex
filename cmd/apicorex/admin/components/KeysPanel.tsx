"use client";

import { useCallback, useEffect, useState } from "react";
import { fetchKeys, issueKey, revokeKey, setSharedKey, type PluginKeysView } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { relativeTime } from "@/lib/utils";
import { KeyRound, ShieldOff, ShieldCheck } from "lucide-react";

// The Jenkins credential a plugin's key goes into. One per plugin, so that a
// plugin's deploy can hold its own key and nobody else's.
function credentialName(plugin: string): string {
  return plugin.toUpperCase().replace(/-/g, "_") + "_CORE_API_KEY";
}

export default function KeysPanel() {
  const [data, setData] = useState<{ accept_shared: boolean; plugins: PluginKeysView[] } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [issued, setIssued] = useState<{ plugin: string; key: string } | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await fetchKeys());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function issue(plugin: string) {
    try {
      const r = await issueKey(plugin);
      setIssued({ plugin, key: r.key });
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function revoke(plugin: string, id: number, hint: string) {
    if (!window.confirm(`Revoke ${plugin}'s key …${hint}? A process still using it is refused at its next registration or config fetch.`)) return;
    try {
      await revokeKey(plugin, id);
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }

  async function toggleShared(accept: boolean) {
    try {
      await setSharedKey(accept);
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      if (!accept && msg.includes("still use the shared key")) {
        if (!window.confirm(`${msg}.\n\nTurn the shared key off anyway?`)) return;
        try {
          await setSharedKey(false, true);
        } catch (e2) {
          setError(e2 instanceof Error ? e2.message : String(e2));
          return;
        }
      } else {
        setError(msg);
        return;
      }
    }
    load();
  }

  const stillShared = data?.plugins.filter((p) => p.registered && p.auth === "shared").map((p) => p.plugin) ?? [];

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-lg font-semibold">API keys</h1>
        <p className="text-sm text-muted-foreground">
          A key of its own for each plugin to register and fetch its config with, in place of the shared
          PLUGIN_API_KEY. With its own key a plugin can register only under its own name and read only its
          own config. It goes in the plugin's environment as CORE_API_KEY.
        </p>
      </div>

      {error && <div className="rounded-xl border bg-card px-4 py-3.5 text-sm text-danger">{error}</div>}

      {issued && (
        <div className="rounded-xl border border-ok-foreground/40 bg-ok-surface p-5 text-sm">
          <div className="font-semibold">New key for {issued.plugin} — shown once</div>
          <div className="mt-2 break-all rounded-md bg-background p-3 font-mono text-xs">{issued.key}</div>
          <p className="mt-2 text-xs">
            Put it in a Jenkins <b>Secret text</b> credential named <span className="font-mono">{credentialName(issued.plugin)}</span>{" "}
            and deploy {issued.plugin}; it reaches the plugin as CORE_API_KEY. Core keeps only its hash, so copy it now.
          </p>
          <div className="mt-3 flex gap-2">
            <Button size="sm" variant="outline" onClick={() => navigator.clipboard?.writeText(issued.key)}>
              Copy
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setIssued(null)}>
              Done
            </Button>
          </div>
        </div>
      )}

      {!data ? (
        <Skeleton className="h-40 w-full" />
      ) : (
        <>
          <div className="rounded-xl border bg-card p-5">
            <div className="flex flex-wrap items-start justify-between gap-3">
              <div className="flex flex-col gap-1">
                <div className="flex items-center gap-2 font-semibold">
                  {data.accept_shared ? <ShieldOff className="h-4 w-4" /> : <ShieldCheck className="h-4 w-4" />}
                  Shared PLUGIN_API_KEY {data.accept_shared ? "accepted" : "refused"}
                </div>
                <span className="text-sm text-muted-foreground">
                  {data.accept_shared
                    ? stillShared.length > 0
                      ? `Still used by: ${stillShared.join(", ")}. Turn it off once every plugin has its own key.`
                      : "No registered plugin uses it any more; it can be turned off."
                    : "Only plugins' own keys are accepted."}
                </span>
              </div>
              <Button size="sm" variant={data.accept_shared ? "destructive" : "outline"} onClick={() => toggleShared(!data.accept_shared)}>
                {data.accept_shared ? "Stop accepting it" : "Accept it again"}
              </Button>
            </div>
          </div>

          <div className="rounded-xl border bg-card">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-left text-xs text-muted-foreground">
                  <th className="px-5 py-2 font-medium">Plugin</th>
                  <th className="py-2 pr-3 font-medium">Running with</th>
                  <th className="py-2 pr-3 font-medium">Keys</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {data.plugins.map((p) => (
                  <tr key={p.plugin} className="border-t align-top">
                    <td className="px-5 py-2.5 font-medium">{p.plugin}</td>
                    <td className="py-2.5 pr-3">
                      {!p.registered ? (
                        <Badge variant="outline">not registered</Badge>
                      ) : p.auth === "own" ? (
                        <Badge variant="ok">its own key</Badge>
                      ) : (
                        <Badge variant="warn">shared key</Badge>
                      )}
                    </td>
                    <td className="py-2.5 pr-3">
                      {p.keys.length === 0 ? (
                        <span className="text-xs text-muted-foreground">none</span>
                      ) : (
                        <div className="flex flex-col gap-1">
                          {p.keys.map((k) => (
                            <div key={k.id} className="flex flex-wrap items-center gap-2 text-xs">
                              <span className="font-mono">…{k.hint}</span>
                              {k.revoked_at ? (
                                <span className="text-muted-foreground">revoked {relativeTime(k.revoked_at)}</span>
                              ) : (
                                <>
                                  <span className="text-muted-foreground">
                                    issued {relativeTime(k.created_at)} · {k.last_used_at ? `last used ${relativeTime(k.last_used_at)}` : "never used"}
                                  </span>
                                  <button className="text-danger underline" onClick={() => revoke(p.plugin, k.id, k.hint)}>
                                    revoke
                                  </button>
                                </>
                              )}
                            </div>
                          ))}
                        </div>
                      )}
                    </td>
                    <td className="py-2.5 pr-5 text-right">
                      <Button size="sm" variant="outline" onClick={() => issue(p.plugin)}>
                        <KeyRound /> Issue key
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </div>
  );
}
