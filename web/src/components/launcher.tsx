import { AppWindow } from "lucide-react";
import { type ReactNode, useState } from "react";
import { useTopic } from "@/api/stream";
import type { App, Group } from "@/api/types";
import { Panel, SourceChips } from "@/components/panel";
import { Skeleton } from "@/components/ui/skeleton";
import { type InfraLink, useInfraLinks } from "@/config/links";
import { cn } from "@/lib/utils";
import { usePluginsState, usePluginVisible, useTopicAvailable } from "@/plugins/state";

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

const OTHER = "Other";

interface SectionProps {
  title: string;
  /** What the tiles are, for the screen-reader label ("apps", "links"). */
  noun: string;
  /** Tiles that match the search. */
  count: number;
  /** Tiles in the section without a search. */
  total: number;
  compact?: boolean;
  children: ReactNode;
}

/**
 * A titled grid of tiles. Its count ("12 of 14" while a search narrows it) is its own polite live
 * region, so a search that changes the section is announced, and a section a plugin adds or
 * removes brings or takes its announcement with it. The compact overview row stays silent.
 */
function Section({ title, noun, count, total, compact, children }: SectionProps) {
  const text = count === total ? String(count) : `${count} of ${total}`;
  return (
    // A section a search empties is hidden from sight but not from assistive tech: its count
    // must stay mounted to be announced when it drops to "0 of N" and when it comes back.
    <section className={cn("space-y-2", count === 0 && "sr-only")}>
      <h3 className="flex items-center gap-2 text-xs font-medium tracking-wide text-muted uppercase">
        {title}
        <span
          role={compact ? undefined : "status"}
          aria-live={compact ? undefined : "polite"}
          aria-atomic={compact ? undefined : "true"}
          className="num rounded-full border border-line px-1.5 text-[10px] text-faint"
        >
          {!compact && (
            <span className="sr-only">{`${noun} matching the search in ${title}: `}</span>
          )}
          {text}
        </span>
      </h3>
      {count > 0 && <div className={GRID}>{children}</div>}
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

interface SectionsProps {
  /** Filter text (name, description, group, hostname). */
  query: string;
  /** The overview's launcher row: smaller descriptions, and the counts are not announced. */
  compact?: boolean;
}

/** The Kubernetes apps, grouped Platform/Apps/Tenants (then any other group), filtered by `query`. */
export function AppsSection({ query, compact }: SectionsProps) {
  const env = useTopic("kubernetes.apps");
  const apps = env?.data.apps;
  if (!apps) return <LauncherSkeleton />;

  const known = new Set<string>(GROUP_ORDER);
  const inGroup = (name: string, a: App) =>
    name === OTHER ? !known.has(a.group) : a.group === name;
  const groups = [...GROUP_ORDER, OTHER]
    .map((name) => ({
      name,
      total: apps.filter((a) => inGroup(name, a)).length,
      items: apps.filter((a) => inGroup(name, a) && appMatches(a, query)),
    }))
    .filter((g) => g.total > 0); // a group a search empties stays mounted, at "0 of N"

  return (
    <>
      {!compact && env.sources.some((s) => !s.ok) && (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <SourceChips sources={env.sources} />
        </div>
      )}
      {groups.map((g) => (
        <Section
          key={g.name}
          title={g.name}
          noun="apps"
          count={g.items.length}
          total={g.total}
          compact={compact}
        >
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
    </>
  );
}

/** The infrastructure links of the launcher plugin, filtered by `query`. */
export function InfraSection({ query, compact }: SectionsProps) {
  const { links: infra } = useInfraLinks();
  if (infra.length === 0) return null;
  const items = infra.filter((l) => infraMatches(l, query));
  return (
    <Section
      title="Infrastructure"
      noun="links"
      count={items.length}
      total={infra.length}
      compact={compact}
    >
      {items.map((l) => (
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
  );
}

export type LauncherStatus = "loading" | "shown" | "off";

/**
 * Whether either launcher section is on the page. "loading" until the plugin list (and, for the
 * infrastructure links, the launcher.links topic) is known, so nothing claims "off" while data is
 * on its way.
 */
export function useLauncherStatus(): LauncherStatus {
  const { ready } = usePluginsState();
  const apps = useTopicAvailable("kubernetes.apps");
  const launcher = usePluginVisible("launcher");
  const infra = useInfraLinks();
  if (apps === true || (launcher === true && infra.links.length > 0)) return "shown";
  if (!ready || (launcher === true && infra.pending)) return "loading";
  return "off";
}

/** Whether either launcher section is on the page (the overview drops its heading when not). */
export function useLauncherShown(): boolean {
  return useLauncherStatus() === "shown";
}

/**
 * The Apps page's empty state, decided from both sections: it speaks only when something is
 * listed and nothing matches, and never while a section's data has yet to arrive. The status
 * element is always mounted so that its text is announced when it changes; an empty one takes no
 * room.
 */
export function NoMatches({ query }: { query: string }) {
  const appsOn = useTopicAvailable("kubernetes.apps") === true;
  const launcherOn = usePluginVisible("launcher") === true;
  const apps = useTopic("kubernetes.apps")?.data.apps;
  const infra = useInfraLinks();
  const loading = (appsOn && apps === undefined) || (launcherOn && infra.pending);
  const listedApps = appsOn ? (apps ?? []) : [];
  const listedInfra = launcherOn ? infra.links : [];
  const nothing =
    !loading &&
    listedApps.length + listedInfra.length > 0 &&
    !listedApps.some((a) => appMatches(a, query)) &&
    !listedInfra.some((l) => infraMatches(l, query));
  return (
    <p role="status" className={cn("text-center text-sm text-muted", nothing && "py-8")}>
      {nothing ? `No apps match “${query.trim()}”.` : ""}
    </p>
  );
}
