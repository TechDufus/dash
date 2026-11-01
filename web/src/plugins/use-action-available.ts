import { useMe } from "@/api/queries";
import type { PluginInfo } from "@/api/types";
import { isVisible, usePluginsState } from "./state";

/** Role ranks: admin includes viewer. A role this build does not know ranks below every other. */
const RANK: Readonly<Record<string, number>> = { viewer: 1, admin: 2 };

/**
 * Whether `role` may use `action` of the plugin whose `core.plugins` row is `row`: the row is
 * visible and lists the action (so `actions.enabled` is on), and the role meets the action's
 * `minRole`. An unknown or missing `minRole` counts as admin: the server stays the authority, the
 * SPA only declines to offer controls it cannot be sure about.
 */
export function actionAvailable(
  row: PluginInfo | undefined,
  action: string,
  role: string | undefined,
): boolean {
  if (row === undefined || !isVisible(row.state)) return false;
  const info = row.actions.find((a) => a.name === action);
  if (info === undefined) return false;
  const need = RANK[info.minRole] ?? RANK.admin ?? 0;
  const have = role === undefined ? 0 : (RANK[role] ?? 0);
  return have >= need;
}

/**
 * Whether to show the controls of a plugin action. It reads the live plugin row (so switching
 * `actions.enabled` on or off shows at once) and `me.role`; it does not read `me.scopes`, which
 * derive from the same data but are fetched once and would never see the change.
 */
export function useActionAvailable(pluginId: string, action: string): boolean {
  const { byId } = usePluginsState();
  const me = useMe();
  return actionAvailable(byId.get(pluginId), action, me.data?.role);
}
