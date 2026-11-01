import {
  PluginBlocked,
  PluginDisabled,
  type PluginState,
  PluginUnavailable,
  PluginUnconfigured,
} from "@/api/types";

const HIDDEN: ReadonlySet<string> = new Set([
  PluginDisabled,
  PluginUnavailable,
  PluginUnconfigured,
  PluginBlocked,
]);

/**
 * A plugin's pages are visible in the states starting, healthy, degraded and error, and hidden in
 * disabled, unavailable, unconfigured and blocked (design A.9). A state this build does not know
 * is visible: a newer server must not make an older page vanish.
 *
 * It lives apart from state.ts because the stream store needs it to prune, and state.ts reads the
 * stream store.
 */
export function isVisible(state: PluginState): boolean {
  return !HIDDEN.has(state);
}
