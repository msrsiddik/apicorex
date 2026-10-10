"use client";

import { cn } from "@/lib/utils";
import { clearSession } from "@/lib/api";
import {
  LayoutDashboard,
  Plug,
  FileText,
  Activity,
  LogOut,
  Database,
  ScrollText,
  SlidersHorizontal,
  KeyRound,
  Archive,
  ShieldCheck,
  PanelLeft,
  PanelLeftClose,
} from "lucide-react";

export type SectionId = "overview" | "plugins" | "database" | "protection" | "settings" | "keys" | "backups" | "audit";

const NAV: { id: SectionId; label: string; Icon: typeof Plug }[] = [
  { id: "overview", label: "Overview", Icon: LayoutDashboard },
  { id: "plugins", label: "Plugins & routes", Icon: Plug },
  { id: "database", label: "Databases", Icon: Database },
  { id: "protection", label: "Protection", Icon: ShieldCheck },
  { id: "settings", label: "Settings", Icon: SlidersHorizontal },
  { id: "keys", label: "API keys", Icon: KeyRound },
  { id: "backups", label: "Store backups", Icon: Archive },
  { id: "audit", label: "Audit log", Icon: ScrollText },
];

const LINKS: { href: string; label: string; Icon: typeof Plug; target?: string }[] = [
  { href: "/docs", label: "API docs", Icon: FileText, target: "apicorex_docs" },
  { href: "/metrics", label: "Raw metrics", Icon: Activity },
];

export default function Sidebar({
  active,
  onNavigate,
  onLogout,
  collapsed,
  onToggleCollapse,
}: {
  active: SectionId;
  onNavigate: (id: SectionId) => void;
  onLogout: () => void;
  collapsed: boolean;
  onToggleCollapse: () => void;
}) {
  const item = (on: boolean) =>
    cn(
      "flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm transition-colors",
      collapsed && "justify-center px-0",
      on ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-secondary hover:text-foreground",
    );
  return (
    <aside
      className={cn(
        "flex shrink-0 flex-col border-r border-border bg-card transition-[width] duration-200",
        collapsed ? "w-[60px]" : "w-[228px]",
      )}
    >
      {/* Brand + collapse. Collapsed, the two stack: side by side they need
          more than the 60px the rail has. */}
      <div className={cn("flex items-center gap-2 py-3.5", collapsed ? "flex-col px-0" : "px-3")}>
        <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-primary text-sm font-semibold text-primary-foreground">
          C
        </span>
        {!collapsed && <span className="flex-1 truncate text-sm font-semibold">ApiCoreX Gateway</span>}
        <button
          onClick={onToggleCollapse}
          aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
        >
          {collapsed ? <PanelLeft className="h-4 w-4" /> : <PanelLeftClose className="h-4 w-4" />}
        </button>
      </div>

      <nav className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto px-2 py-2">
        {!collapsed && (
          <span className="px-2 pb-1 pt-2 text-[11px] uppercase tracking-wide text-muted-foreground">Gateway</span>
        )}
        {NAV.map(({ id, label, Icon }) => (
          <button key={id} onClick={() => onNavigate(id)} title={collapsed ? label : undefined} className={item(active === id)}>
            <Icon className="h-4 w-4 shrink-0" />
            {!collapsed && <span className="truncate">{label}</span>}
          </button>
        ))}

        {collapsed ? (
          <div className="mx-2 my-2 border-t border-border" />
        ) : (
          <span className="px-2 pb-1 pt-4 text-[11px] uppercase tracking-wide text-muted-foreground">Links</span>
        )}
        {LINKS.map(({ href, label, Icon, target }) => (
          <a
            key={href}
            href={href}
            target={target}
            rel={target ? "noopener noreferrer" : undefined}
            title={collapsed ? label : undefined}
            className={item(false)}
          >
            <Icon className="h-4 w-4 shrink-0" />
            {!collapsed && <span className="truncate">{label}</span>}
          </a>
        ))}
      </nav>

      <div className="border-t border-border p-2">
        <button
          onClick={() => {
            clearSession();
            onLogout();
          }}
          title={collapsed ? "Log out" : undefined}
          className={cn(item(false), "w-full")}
        >
          <LogOut className="h-4 w-4 shrink-0" />
          {!collapsed && <span className="truncate">Log out</span>}
        </button>
      </div>
    </aside>
  );
}
