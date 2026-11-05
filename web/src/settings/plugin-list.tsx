import { useQueryClient } from "@tanstack/react-query";
import { Lock } from "lucide-react";
import { type JSX, useState } from "react";
import { toast } from "sonner";
import { Link } from "wouter";
import { api, errorMessage } from "@/api/client";
import type { SettingsPlugin, SettingsView } from "@/api/types";
import { HealthBadge } from "@/components/status";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { usePluginsState } from "@/plugins/state";
import { invalidateSettings, settingsPaths } from "./api";
import { lockedText, stateHealth } from "./list-locked";

/**
 * The plugins of GET /settings, each with its live state: the state and message come from
 * core.plugins as soon as the stream (or GET /plugins) has a row for the plugin, so a row changes
 * without a reload; the view's own state is the fallback. The toggle writes `enabled` with the
 * revision of the last read and refreshes every settings read afterwards, failed or not (a 412
 * leaves the next attempt the fresh revision).
 */
export function PluginList({ view }: { view: SettingsView }): JSX.Element {
  const { byId } = usePluginsState();
  const queryClient = useQueryClient();
  const [pending, setPending] = useState<ReadonlySet<string>>(new Set());

  async function toggle(id: string, enabled: boolean) {
    setPending((s) => new Set(s).add(id));
    try {
      await api.put(settingsPaths.plugin(id), { enabled }, { ifMatch: view.revision });
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      await invalidateSettings(queryClient);
      setPending((s) => {
        const next = new Set(s);
        next.delete(id);
        return next;
      });
    }
  }

  return (
    <section aria-labelledby="settings-plugins" className="space-y-2">
      <h2 id="settings-plugins" className="text-sm font-semibold text-fg">
        Plugins
      </h2>
      <ul className="divide-y divide-line rounded-lg border border-line bg-panel">
        {view.plugins.map((p) => {
          const live = byId.get(p.id);
          return (
            <PluginRow
              key={p.id}
              plugin={p}
              state={live?.state ?? p.state}
              message={live?.message ?? p.message}
              readonly={view.store.readonly}
              pending={pending.has(p.id)}
              onToggle={(on) => void toggle(p.id, on)}
            />
          );
        })}
      </ul>
    </section>
  );
}

interface PluginRowProps {
  plugin: SettingsPlugin;
  state: string;
  message: string;
  readonly: boolean;
  pending: boolean;
  onToggle: (enabled: boolean) => void;
}

function PluginRow({ plugin, state, message, readonly, pending, onToggle }: PluginRowProps) {
  const f = plugin.enabled;
  const lock = lockedText(f) ?? (readonly ? "Settings are read-only" : undefined);
  const switchId = `plugin-enabled-${plugin.id}`;
  const lockId = `${switchId}-lock`;
  const toggle = (
    <Switch
      id={switchId}
      // A set `enabled` shows its value; an unset one follows the live core.plugins state, which
      // is fresher than the value of the last settings read.
      checked={f.set ? f.value === true : state !== "disabled"}
      disabled={lock !== undefined || pending}
      aria-describedby={lock ? lockId : undefined}
      onCheckedChange={onToggle}
    />
  );
  return (
    <li className="flex flex-wrap items-center gap-3 px-4 py-3" data-plugin={plugin.id}>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium text-fg">{plugin.name}</span>
          <HealthBadge health={stateHealth(state)} label={state} />
        </div>
        {message && message !== state ? <p className="text-xs text-muted">{message}</p> : null}
      </div>
      {plugin.configurable ? (
        <Link
          href={`/settings/plugins/${encodeURIComponent(plugin.id)}`}
          className="text-sm text-accent underline-offset-4 hover:underline"
        >
          Configure <span className="sr-only">{plugin.name}</span>
        </Link>
      ) : null}
      <div className="flex items-center gap-2">
        <Label htmlFor={switchId} className="text-xs text-muted">
          <span className="sr-only">{plugin.name}</span> enabled
        </Label>
        {lock ? (
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                aria-label={`Locked: ${lock}`}
                className="inline-flex rounded-sm text-faint focus-visible:outline-2 focus-visible:outline-accent"
              >
                <Lock className="size-3.5" aria-hidden="true" />
              </button>
            </TooltipTrigger>
            <TooltipContent>{lock}</TooltipContent>
          </Tooltip>
        ) : null}
        {toggle}
        {lock ? (
          <span id={lockId} className="sr-only">
            {lock}
          </span>
        ) : null}
      </div>
    </li>
  );
}
