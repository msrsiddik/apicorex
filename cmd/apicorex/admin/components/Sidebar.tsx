"use client";

import { cn } from "@/lib/utils";
import { clearSession } from "@/lib/api";
import { LayoutDashboard, Plug, FileText, Activity, LogOut } from "lucide-react";

export type SectionId = "overview" | "plugins";

const NAV: { id: SectionId; label: string; Icon: typeof Plug }[] = [
  { id: "overview", label: "Overview", Icon: LayoutDashboard },
  { id: "plugins", label: "Plugins & routes", Icon: Plug },
];

export default function Sidebar({
  active,
  onNavigate,
  onLogout,
}: {
  active: SectionId;
  onNavigate: (id: SectionId) => void;
  onLogout: () => void;
}) {
  return (
    <aside className="flex w-[210px] shrink-0 flex-col border-r border-border bg-card">
      <div className="flex items-center gap-2 px-3 py-3.5">
        <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary text-sm font-semibold text-primary-foreground">
          C
        </span>
        <span className="flex-1 truncate text-sm font-semibold">
          ApiCoreX Gateway
        </span>
      </div>

      <nav className="flex flex-1 flex-col gap-0.5 px-2 py-2">
        <span className="px-2 pb-1 pt-2 text-[11px] uppercase tracking-wide text-muted-foreground">
          Gateway
        </span>
        {NAV.map(({ id, label, Icon }) => {
          const on = active === id;
          return (
            <button
              key={id}
              onClick={() => onNavigate(id)}
              className={cn(
                "flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm transition-colors",
                on
                  ? "bg-accent text-accent-foreground"
                  : "text-muted-foreground hover:bg-secondary hover:text-foreground",
              )}
            >
              <Icon className="h-4 w-4 shrink-0" />
              <span className="truncate">{label}</span>
            </button>
          );
        })}

        <span className="px-2 pb-1 pt-4 text-[11px] uppercase tracking-wide text-muted-foreground">
          Links
        </span>
        <a
          href="/docs"
          target="apicorex_docs"
          rel="noopener noreferrer"
          className="flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
        >
          <FileText className="h-4 w-4 shrink-0" />
          <span className="truncate">API docs</span>
        </a>
        <a
          href="/metrics"
          className="flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
        >
          <Activity className="h-4 w-4 shrink-0" />
          <span className="truncate">Raw metrics</span>
        </a>
      </nav>

      <div className="border-t border-border p-2">
        <button
          onClick={() => {
            clearSession();
            onLogout();
          }}
          className="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
        >
          <LogOut className="h-4 w-4 shrink-0" />
          <span className="truncate">Log out</span>
        </button>
      </div>
    </aside>
  );
}
