"use client";

import { useCallback, useEffect, useState } from "react";
import { fetchCommands, queueCommand, type CommandKind, type PluginCommand } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { relativeTime } from "@/lib/utils";
import { Power, RefreshCw } from "lucide-react";

// Plugins whose restart is felt beyond themselves, and how. A second
// confirmation names the cost before anyone pays it.
const RESTART_WARNINGS: Record<string, string> = {
  identity: "Identity handles every login. While it restarts, nobody can sign in and signed-in requests may fail for a few seconds.",
};

const STATE_VARIANT: Record<PluginCommand["state"], "ok" | "warn" | "danger" | "default"> = {
  pending: "warn",
  delivered: "warn",
  done: "ok",
  failed: "danger",
  superseded: "default",
  expired: "default",
};

const STATE_TEXT: Record<PluginCommand["state"], string> = {
  pending: "waiting for heartbeat",
  delivered: "running",
  done: "done",
  failed: "failed",
  superseded: "replaced by a newer command",
  expired: "expired — the plugin never collected it",
};

export default function PluginCommands({ plugin }: { plugin: string }) {
  const [commands, setCommands] = useState<PluginCommand[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setCommands(await fetchCommands(plugin));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [plugin]);

  useEffect(() => {
    load();
  }, [load]);

  // Poll only while something is in flight: a command reaches the plugin on
  // its next heartbeat, and the result follows within seconds of that.
  const inFlight = commands?.some((c) => c.state === "pending" || c.state === "delivered");
  useEffect(() => {
    if (!inFlight) return;
    const id = setInterval(load, 2000);
    return () => clearInterval(id);
  }, [inFlight, load]);

  async function send(kind: CommandKind) {
    const what =
      kind === "restart"
        ? `Restart "${plugin}"? It finishes the requests it is serving, exits, and is started again by its supervisor. Callers get 503 "restarting" until it is back.`
        : `Reload "${plugin}"'s database config? It opens a pool with the current settings and switches to it only if that pool works. A plugin that cannot swap its pool restarts instead.`;
    if (!window.confirm(what)) return;
    const warning = RESTART_WARNINGS[plugin];
    if (warning && !window.confirm(`${warning}\n\nGo ahead?`)) return;

    setBusy(true);
    setError(null);
    try {
      await queueCommand(plugin, kind);
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex flex-col gap-2 px-4 pt-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" variant="outline" disabled={busy || inFlight} onClick={() => send("reload")}>
          <RefreshCw className="h-3.5 w-3.5" />
          Reload DB config
        </Button>
        <Button size="sm" variant="destructive" disabled={busy || inFlight} onClick={() => send("restart")}>
          <Power className="h-3.5 w-3.5" />
          Restart
        </Button>
        {inFlight && <span className="text-xs text-muted-foreground">A command is in flight; it reaches the plugin on its next heartbeat.</span>}
      </div>
      {error && <p className="text-xs text-danger">{error}</p>}
      {commands && commands.length > 0 && (
        <table className="w-full text-xs">
          <tbody>
            {commands.map((c) => (
              <tr key={c.id} className="border-t border-border/50">
                <td className="py-1 pr-3 font-mono">{c.kind}</td>
                <td className="py-1 pr-3">
                  <Badge variant={STATE_VARIANT[c.state]}>{STATE_TEXT[c.state]}</Badge>
                </td>
                <td className="py-1 pr-3 text-muted-foreground">{c.result || "—"}</td>
                <td className="py-1 text-right text-muted-foreground">
                  {relativeTime(c.requested_at)} · {c.requested_by}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
