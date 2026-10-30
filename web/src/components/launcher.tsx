import { AppWindow, LayoutGrid } from "lucide-react";
import { type ReactNode, useState } from "react";
import type { App, Group, SourceStatus } from "@/api/types";
import { Panel, SourceChips } from "@/components/panel";
import { Skeleton } from "@/components/ui/skeleton";
import type { InfraLink } from "@/config/links";
import { cn } from "@/lib/utils";

const GROUP_ORDER: Group[] = ["Platform", "Apps", "Tenants"];
const GRID = "grid grid-cols-[repeat(auto-fill,minmax(210px,1fr))] gap-3";

/** Hostname of a URL, or "" when it does not parse. */
export function hostnameOf(href: string): string {
  try {
    return new URL(href).hostname;
  } catch {
    return "";
  }
}

/** Case-insensitive match on name, description, group and hostname. */
export function appMatches(app: App, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (q === "") return true;
  return [app.name, app.description, app.group, hostnameOf(app.href)].some((s) =>
    s.toLowerCase().includes(q),
  );
}

export function infraMatches(link: InfraLink, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (q === "") return true;
  return [link.name, link.description, hostnameOf(link.href)].some((s) =>
    s.toLowerCase().includes(q),
  );
}

function TileIcon({ src }: { src: string }) {
  const [failed, setFailed] = useState(false);
  if (!src || failed) {
    return (
      <span className="flex size-7 shrink-0 items-center justify-center rounded-md bg-accent/10 text-accent">
        <AppWindow className="size-4" aria-hidden="true" />
      </span>
    );
  }
  return (
    <img
      src={src}
      alt=""
      aria-hidden="true"
      loading="lazy"
      width={28}
      height={28}
      onError={() => setFailed(true)}
      className="size-7 shrink-0 rounded-md object-contain"
    />
  );
}

function HealthMark({ name, healthy }: { name: string; healthy: boolean | undefined }) {
  const state = healthy === true ? "healthy" : healthy === false ? "down" : "no health check";
  return (
    <span
      role="img"
      aria-label={`${name}: ${state}`}
      title={state}
      className={cn(
        "size-2 shrink-0 rounded-full",
        healthy === true && "bg-ok shadow-[0_0_8px_var(--color-ok)]",
        healthy === false && "bg-crit shadow-[0_0_8px_var(--color-crit)]",
        healthy === undefined && "bg-faint",
      )}
    />
  );
}

interface TileProps {
  name: string;
  description: string;
  href: string;
  icon: string;
  /** Omit for tiles without a health check (infrastructure links). */
  health?: { healthy: boolean | undefined };
  compact?: boolean;
}

function Tile({ name, description, href, icon, health, compact }: TileProps) {
  return (
    <a href={href} target="_blank" rel="noreferrer" className="group block rounded-2xl">
      <Panel interactive className="flex h-full items-start gap-3 p-3">
        <TileIcon key={icon} src={icon} />
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-2">
            <span className="truncate text-sm font-medium text-fg">{name}</span>
            {health && (
              <span className="ml-auto">
                <HealthMark name={name} healthy={health.healthy} />
              </span>
            )}
          </span>
          <span
            className={cn(
              "mt-0.5 block text-xs text-muted",
              compact ? "line-clamp-1" : "line-clamp-2",
            )}
          >
            {description}
          </span>
        </span>
      </Panel>
    </a>
  );
}

function Section({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: ReactNode;
}) {
  return (
    <section className="space-y-2">
      <h3 className="flex items-center gap-2 text-xs font-medium tracking-wide text-muted uppercase">
        {title}
        <span className="num rounded-full border border-line px-1.5 text-[10px] text-faint">
          {count}
        </span>
      </h3>
      <div className={GRID}>{children}</div>
    </section>
  );
}

/** Placeholder tiles shown until the apps topic arrives. */
export function LauncherSkeleton({ count = 8 }: { count?: number }) {
  return (
    <div className={GRID} aria-busy="true">
      {Array.from({ length: count }, (_, i) => (
        // biome-ignore lint/suspicious/noArrayIndexKey: static placeholder tiles
        <Skeleton key={i} className="h-[66px] rounded-2xl" />
      ))}
    </div>
  );
}

interface LauncherProps {
  /** undefined until the apps topic arrives (renders skeleton tiles). */
  apps: App[] | undefined;
  infra: InfraLink[];
  sources?: SourceStatus[];
  /** Section title above the tiles (Overview); omit when the page supplies its own header. */
  title?: string;
  /** Filter text (name, description, group, hostname). */
  query?: string;
  compact?: boolean;
  /** Shown when nothing matches the query. */
  emptyText?: string;
}

/** App tiles grouped by Platform/Apps/Tenants, then the Infrastructure links. */
export function Launcher({
  apps,
  infra,
  sources,
  title,
  query = "",
  compact,
  emptyText = "Nothing matches your search.",
}: LauncherProps) {
  const header =
    title || sources?.some((s) => !s.ok) ? (
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {title && (
          <h2 className="flex items-center gap-2 text-sm font-medium tracking-wide text-fg">
            <LayoutGrid className="size-4 text-accent" aria-hidden="true" />
            {title}
          </h2>
        )}
        <SourceChips sources={sources} />
      </div>
    ) : null;

  if (!apps) {
    return (
      <div className="space-y-3">
        {header}
        <LauncherSkeleton />
      </div>
    );
  }

  const visible = apps.filter((a) => appMatches(a, query));
  const infraVisible = infra.filter((l) => infraMatches(l, query));
  const known = new Set<string>(GROUP_ORDER);
  const groups: { name: string; items: App[] }[] = [
    ...GROUP_ORDER.map((name) => ({ name, items: visible.filter((a) => a.group === name) })),
    {
      name: "Other",
      items: visible.filter((a) => !known.has(a.group)),
    },
  ].filter((g) => g.items.length > 0);

  return (
    <div className="space-y-5">
      {header}
      {groups.map((g) => (
        <Section key={g.name} title={g.name} count={g.items.length}>
          {g.items.map((a) => (
            <Tile
              key={`${a.namespace}/${a.name}/${a.href}`}
              name={a.name}
              description={a.description}
              href={a.href}
              icon={a.iconUrl}
              health={{ healthy: a.healthy }}
              compact={compact}
            />
          ))}
        </Section>
      ))}
      {infraVisible.length > 0 && (
        <Section title="Infrastructure" count={infraVisible.length}>
          {infraVisible.map((l) => (
            <Tile
              key={l.href}
              name={l.name}
              description={l.description}
              href={l.href}
              icon={l.icon}
              compact={compact}
            />
          ))}
        </Section>
      )}
      {groups.length === 0 && infraVisible.length === 0 && (
        <p className="py-8 text-center text-sm text-muted">{emptyText}</p>
      )}
    </div>
  );
}
