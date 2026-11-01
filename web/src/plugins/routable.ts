import { useCallback } from "react";
import { matchRoute, useRouter } from "wouter";
import { CORE_PAGES, moduleById } from "./registry";
import { isVisible, usePluginsState } from "./state";

/**
 * Whether an attention item's route is worth a link: its plugin is visible and the SPA has a page
 * for the route (a module route, the plugin's generic page, or a core page). An item that names no
 * plugin belongs to the core, so its route stays a link.
 */
export function useRoutable(): (plugin: string, route: string) => boolean {
  const { byId } = usePluginsState();
  const { parser } = useRouter();
  return useCallback(
    (plugin, route) => {
      if (plugin === "") return true;
      const info = byId.get(plugin);
      if (!info || !isVisible(info.state)) return false;
      const path = route.split(/[?#]/, 1)[0] ?? route;
      if (CORE_PAGES.some((c) => c.path === path)) return true;
      const mod = moduleById.get(plugin);
      if (mod) return (mod.routes ?? []).some((r) => matchRoute(parser, r.path, path)[0]);
      return info.generic.length > 0 && path === `/x/${plugin}`;
    },
    [byId, parser],
  );
}
