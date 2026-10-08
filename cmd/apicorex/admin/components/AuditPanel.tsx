"use client";

import { useCallback, useEffect, useState } from "react";
import { fetchAudit, type AuditEntry } from "@/lib/api";
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
import { ScrollText } from "lucide-react";

export default function AuditPanel() {
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [more, setMore] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (before?: number) => {
    try {
      const page = await fetchAudit(before);
      setEntries((prev) => (before && prev ? [...prev, ...page] : page));
      setMore(page.length === 50);
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
        <h1 className="text-lg font-semibold">Audit log</h1>
        <p className="text-sm text-muted-foreground">
          Every change made through this dashboard, newest first. Secrets are never written here.
        </p>
      </div>

      {error && (
        <div className="rounded-xl border bg-card px-4 py-3.5 text-sm text-danger">
          Failed to load the audit log: {error}
        </div>
      )}

      {!entries ? (
        <div className="flex flex-col gap-2">
          <Skeleton className="h-11 w-full" />
          <Skeleton className="h-11 w-full" />
        </div>
      ) : entries.length === 0 ? (
        <EmptyState icon={ScrollText} title="Nothing recorded yet" description="Changes appear here as they are made." />
      ) : (
        <div className="rounded-xl border bg-card">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead>Who</TableHead>
                <TableHead>Action</TableHead>
                <TableHead>Target</TableHead>
                <TableHead>Detail</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {entries.map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                    {new Date(e.at).toLocaleString()}
                  </TableCell>
                  <TableCell className="text-xs">{e.actor}</TableCell>
                  <TableCell className="font-mono text-xs">{e.action}</TableCell>
                  <TableCell className="text-xs">{e.target === "*" ? "default" : e.target || "—"}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{e.detail || "—"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}

      {entries && entries.length > 0 && more && (
        <div>
          <Button size="sm" variant="outline" onClick={() => load(entries[entries.length - 1].id)}>
            Load older
          </Button>
        </div>
      )}
    </div>
  );
}
