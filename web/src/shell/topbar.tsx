import { Menu, Search } from "lucide-react";
import { useState } from "react";
import { Link, useLocation } from "wouter";
import { useMe } from "@/api/queries";
import { type ConnectionState, useConnection, useTopic } from "@/api/stream";
import type { Health } from "@/api/types";
import { ui } from "@/api/ui-store";
import { HealthBadge } from "@/components/status";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { formatClock } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { isActive, titleFor, useNavEntries } from "@/plugins/state";

function StatusPill() {
  const overview = useTopic("core.overview")?.data;
  if (!overview) {
    return (
      <span className="rounded-full border border-line bg-panel px-2.5 py-1 text-xs text-muted">
        Loading…
      </span>
    );
  }
  const crit = overview.attention.filter((a) => a.severity === "crit").length;
  const warn = overview.attention.filter((a) => a.severity === "warn").length;
  const health: Health = overview.status;
  const label =
    health === "crit"
      ? `${crit} critical`
      : health === "warn"
        ? `${warn} ${warn === 1 ? "warning" : "warnings"}`
        : health === "ok"
          ? "All systems nominal"
          : "Status unknown";
  return (
    <Link href="/alerts" className="rounded-full" aria-label={`Overall status: ${label}`}>
      <HealthBadge
        health={health}
        label={label}
        className="px-2.5 py-1 text-xs max-sm:gap-0 max-sm:[&>[data-slot=label]]:sr-only"
      />
    </Link>
  );
}

const LIVE_TEXT: Record<ConnectionState, string> = {
  live: "LIVE",
  reconnecting: "RECONNECTING",
  connecting: "CONNECTING",
};

function LiveIndicator() {
  const conn = useConnection();
  return (
    <span
      role="status"
      className="hidden items-center gap-1.5 text-[11px] font-medium tracking-widest text-muted sm:inline-flex"
    >
      <span
        className={cn(
          "size-1.5 rounded-full",
          conn === "live" && "bg-ok shadow-[0_0_8px_rgb(62_230_154/0.9)]",
          conn === "reconnecting" && "bg-warn",
          conn === "connecting" && "bg-faint",
        )}
      />
      {LIVE_TEXT[conn]}
    </span>
  );
}

function Clock() {
  const now = useNow(1000);
  return <span className="num hidden text-sm text-muted md:inline">{formatClock(now)}</span>;
}

const isMac = typeof navigator !== "undefined" && /mac|iphone|ipad/i.test(navigator.platform);

export function TopBar() {
  const [location] = useLocation();
  const [menu, setMenu] = useState(false);
  const me = useMe();
  const { entries } = useNavEntries();
  return (
    <header className="sticky top-0 z-20 flex h-14 items-center gap-3 border-b border-line bg-canvas/60 px-4 backdrop-blur-xl md:px-6">
      <Button
        variant="ghost"
        size="icon"
        className="md:hidden"
        aria-label="Open navigation"
        onClick={() => setMenu(true)}
      >
        <Menu />
      </Button>
      <h1 className="truncate text-base font-semibold tracking-tight text-fg">
        {titleFor(entries, location)}
      </h1>
      <div className="ml-auto flex items-center gap-3">
        <StatusPill />
        <Clock />
        <LiveIndicator />
        <Button
          variant="outline"
          size="sm"
          onClick={() => ui.setPalette(true)}
          aria-label="Open command palette"
        >
          <Search />
          <span className="hidden sm:inline">Search</span>
          <kbd className="num hidden rounded border border-line bg-panel px-1 text-[10px] text-muted sm:inline">
            {isMac ? "⌘K" : "Ctrl K"}
          </kbd>
        </Button>
      </div>
      <Sheet open={menu} onOpenChange={setMenu}>
        <SheetContent side="left" className="w-64 data-[side=left]:w-64 gap-0 p-0">
          <SheetHeader className="border-b border-line">
            <SheetTitle>dash</SheetTitle>
            <SheetDescription>Homelab mission control</SheetDescription>
          </SheetHeader>
          <nav aria-label="Mobile" className="flex-1 space-y-1 p-3">
            {entries.map((item) => {
              const Icon = item.icon;
              const active = isActive(item, location);
              return (
                <Link
                  key={item.key}
                  href={item.path}
                  onClick={() => setMenu(false)}
                  aria-current={active ? "page" : undefined}
                  className={cn(
                    "flex items-center gap-3 rounded-xl px-3 py-2.5 text-sm text-muted hover:bg-panel-hover hover:text-fg",
                    active && "bg-accent/10 text-accent hover:bg-accent/15 hover:text-accent",
                  )}
                >
                  <Icon className="size-4" aria-hidden="true" />
                  {item.label}
                </Link>
              );
            })}
          </nav>
          <div className="border-t border-line p-4 text-xs text-muted">
            {me.data ? (
              <>
                Signed in as <span className="text-fg">{me.data.user}</span>
                <span className="num ml-2 text-faint">{me.data.version}</span>
              </>
            ) : (
              "Signing in…"
            )}
          </div>
        </SheetContent>
      </Sheet>
    </header>
  );
}
