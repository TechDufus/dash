import type { PluginInfo } from "@/api/types";

/**
 * A plugin row as every reader sees it. A v0.5.0 server (an open tab after a rollback, or a pod of
 * a rolling update) sends rows without `generic`; one missing list becomes an empty one here, at
 * the two places rows enter the SPA (the stream and GET /api/v1/plugins), so no reader checks.
 * A row that is already complete is returned as is.
 */
export function normalizePlugin(p: PluginInfo): PluginInfo {
  return Array.isArray(p.generic) ? p : { ...p, generic: [] };
}

export function normalizePlugins(list: readonly PluginInfo[]): PluginInfo[] {
  return list.map(normalizePlugin);
}
