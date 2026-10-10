"use client";

import { useCallback, useEffect, useState } from "react";
import { downloadSnapshot, fetchSnapshots, takeSnapshot, type SnapshotStatus, type StoreSnapshot } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { CloudUpload, Download, HardDriveDownload } from "lucide-react";

function size(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

function when(iso?: string): string {
  return iso ? new Date(iso).toLocaleString() : "—";
}

// A day and a bit: one missed snapshot is the alarm.
const STALE_MS = 26 * 60 * 60 * 1000;

export default function BackupsPanel() {
  const [data, setData] = useState<{ status: SnapshotStatus; snapshots: StoreSnapshot[] } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      setData(await fetchSnapshots());
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function now() {
    setBusy("now");
    try {
      await takeSnapshot();
      await load();
      // The off-site copy runs in the background; look again shortly.
      setTimeout(load, 4000);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  async function download(name: string) {
    setBusy(name);
    try {
      await downloadSnapshot(name);
      load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  }

  const st = data?.status;
  const stale = st && (!st.last_snapshot || Date.now() - new Date(st.last_snapshot).getTime() > STALE_MS);

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-lg font-semibold">Store backups</h1>
        <p className="text-sm text-muted-foreground">
          Copies of Core's own store — database connections, settings, keys, the audit log. Every secret in a
          copy is sealed and opens only with CORE_MASTER_KEY, so keep that key somewhere other than where the
          copies go. To restore: stop Core, put the copy at STORE_PATH, start Core with the same key.
        </p>
      </div>

      {error && <div className="rounded-xl border bg-card px-4 py-3.5 text-sm text-danger">{error}</div>}

      {!data ? (
        <Skeleton className="h-40 w-full" />
      ) : (
        <>
          <div className="rounded-xl border bg-card p-5">
            <div className="grid gap-1 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-muted-foreground">Last snapshot:</span>
                <span>{when(st?.last_snapshot)}</span>
                {stale && <Badge variant="danger">overdue</Badge>}
              </div>
              {st?.last_error && <div className="text-danger">Last attempt failed: {st.last_error}</div>}
              <div>
                <span className="text-muted-foreground">Schedule:</span> every {st?.interval}, newest {st?.keep} kept on this server
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <span className="text-muted-foreground">Off-site copy:</span>
                {st?.remote_configured ? (
                  <>
                    <span className="font-mono text-xs">{st.remote}</span>
                    <span>· last {when(st.last_upload)} · 30 days and 12 months kept there</span>
                  </>
                ) : (
                  <span>off — set STORE_BACKUP_REMOTE and STORE_RCLONE_CONF_B64 for Core</span>
                )}
              </div>
              {st?.last_upload_error && <div className="text-danger">Off-site copy failed: {st.last_upload_error}</div>}
            </div>
            <div className="mt-4">
              <Button onClick={now} disabled={busy !== null}>
                <HardDriveDownload /> Snapshot now
              </Button>
            </div>
          </div>

          <div className="rounded-xl border bg-card">
            {data.snapshots.length === 0 ? (
              <p className="p-5 text-sm text-muted-foreground">No snapshots yet.</p>
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-xs text-muted-foreground">
                    <th className="px-5 py-2 font-medium">Taken</th>
                    <th className="py-2 pr-3 font-medium">Size</th>
                    <th className="py-2 pr-3 font-medium">Off-site</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {data.snapshots.map((s) => (
                    <tr key={s.name} className="border-t">
                      <td className="px-5 py-2">
                        {when(s.created_at)}
                        <div className="font-mono text-xs text-muted-foreground">{s.name}</div>
                      </td>
                      <td className="py-2 pr-3">{size(s.size)}</td>
                      <td className="py-2 pr-3">
                        {s.uploaded ? (
                          <Badge variant="ok" title={when(s.uploaded_at)}>
                            <CloudUpload className="mr-1 h-3 w-3" /> copied
                          </Badge>
                        ) : st?.remote_configured ? (
                          <Badge variant="warn">not yet</Badge>
                        ) : (
                          "—"
                        )}
                      </td>
                      <td className="py-2 pr-5 text-right">
                        <Button size="sm" variant="outline" disabled={busy !== null} onClick={() => download(s.name)}>
                          <Download /> Download
                        </Button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </div>
        </>
      )}
    </div>
  );
}
