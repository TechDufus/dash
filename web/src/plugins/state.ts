import { useQuery } from "@tanstack/react-query";
import type { LucideIcon } from "lucide-react";
import type { ComponentType } from "react";
import { useMemo } from "react";
import { matchRoute, useRouter } from "wouter";
import { api } from "@/api/client";
import { useEnvelope } from "@/api/stream";
import type { PluginInfo, PluginsData } from "@/api/types";
import { iconFor } from "./icons";
import { CORE_PAGES, moduleById, moduleOrder } from "./registry";
import type { PluginModule } from "./types";
import { isVisible } from "./visibility";

export { isVisible };

export interface PluginsState {
  /** A plugin list has arrived, from either source. */
  ready: boolean;
  plugins: readonly PluginInfo[];
  byId: ReadonlyMap<string, PluginInfo>;
}

const NONE: readonly PluginInfo[] = [];

/** The REST copy of the plugin list; main.tsx prefetches it before the first render. */
export const pluginsQuery = {
  queryKey: ["plugins"],
  queryFn: () => api.get<PluginInfo[]>("/api/v1/plugins"),
  staleTime: Number.POSITIVE_INFINITY,
};

/**
 * The plugin rows: core.plugins from the stream once it has spoken, and until then (or when the
 * stream is down at load) GET /api/v1/plugins.
 */
export function usePluginsState(): PluginsState {
  const streamed = useEnvelope("core.plugins");
  const fromStream = (streamed?.data as PluginsData | undefined)?.plugins;
  const query = useQuery({ ...pluginsQuery, enabled: fromStream === undefined });
  const plugins = fromStream ?? query.data;
  return useMemo(() => {
    const list = plugins ?? NONE;
    return {
      ready: plugins !== undefined,
      plugins: list,
      byId: new Map(list.map((p) => [p.id, p])),
    };
  }, [plugins]);
}

/** Whether a plugin's pages are shown; undefined while the plugin list is loading. */
export function usePluginVisible(id: string): boolean | undefined {
  const { ready, byId } = usePluginsState();
  if (!ready) return undefined;
  const info = byId.get(id);
  return info !== undefined && isVisible(info.state);
}

/**
 * A topic is available when a visible plugin lists it; core.* topics always are. Undefined while
 * the plugin list is loading.
 */
export function useTopicAvailable(topic: string): boolean | undefined {
  const { ready, plugins } = usePluginsState();
  if (topic.startsWith("core.")) return true;
  if (!ready) return undefined;
  return plugins.some((p) => isVisible(p.state) && p.topics.includes(topic));
}

export interface NavEntry {
  /** Plugin id + path, or the core page key. */
  key: string;
  path: string;
  label: string;
  /** The topbar title. */
  title: string;
  icon: LucideIcon;
  order: number;
  badge?: ComponentType;
  core: boolean;
}

const warned = new Set<string>();

/**
 * Whether the SPA can draw a nav entry of `info`: its module has a route matching the path, or the
 * plugin declares generic views and the path is its generic page, /x/<id>.
 */
function canRender(
  info: PluginInfo,
  mod: PluginModule | undefined,
  path: string,
  parser: Parameters<typeof matchRoute>[0],
): boolean {
  if (info.generic.length > 0 && path === `/x/${info.id}`) return true;
  return (mod?.routes ?? []).some((r) => matchRoute(parser, r.path, path)[0]);
}

/**
 * The rail, mobile sheet and palette entries: the core pages, plus every nav entry of a visible
 * plugin that a module can render. A backend entry with no module is a build mismatch, not a user
 * error: it is dropped with one console warning.
 */
export function useNavEntries(): { ready: boolean; entries: NavEntry[] } {
  const { ready, plugins } = usePluginsState();
  const { parser } = useRouter();
  const entries = useMemo(() => {
    const out: NavEntry[] = CORE_PAGES.map((c) => ({
      key: c.key,
      path: c.path,
      label: c.label,
      title: c.title,
      icon: c.icon,
      order: c.order,
      core: true,
    }));
    for (const info of plugins) {
      if (!isVisible(info.state)) continue;
      const mod = moduleById.get(info.id);
      for (const n of info.nav) {
        if (!canRender(info, mod, n.path, parser)) {
          const key = `${info.id}:${n.path}`;
          if (!warned.has(key)) {
            warned.add(key);
            console.warn(
              `dash: plugin ${info.id} has a nav entry ${n.path} that no SPA module renders`,
            );
          }
          continue;
        }
        out.push({
          key: `${info.id}:${n.path}`,
          path: n.path,
          label: n.label,
          title: n.title,
          icon: iconFor(n.icon),
          order: mod ? moduleOrder(mod) : 1000,
          badge: mod?.badge?.[n.path],
          core: false,
        });
      }
    }
    return out.sort((a, b) => a.order - b.order);
  }, [plugins, parser]);
  return { ready, entries };
}

/** An entry is active on its own path and below it ("/" only on "/"). */
export function isActive(e: NavEntry, location: string): boolean {
  return e.path === "/"
    ? location === "/"
    : location === e.path || location.startsWith(`${e.path}/`);
}

export function titleFor(entries: readonly NavEntry[], location: string): string {
  return entries.find((e) => isActive(e, location))?.title ?? "dash";
}
