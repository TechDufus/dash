import { useQueryClient } from "@tanstack/react-query";
import { ShieldCheck } from "lucide-react";
import { type JSX, useId, useState } from "react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { SettingsPlugin, SettingsView } from "@/api/types";
import { Panel, PanelHeader } from "@/components/panel";
import { Skeleton } from "@/components/ui/skeleton";
import { invalidateSettings, settingsPaths, useSettingsPlugin } from "./api";
import { liveLockText, sourceText } from "./list-locked";

/** The plugins whose actions agents can call through MCP (design O.1). */
export const AGENT_POLICY_PLUGINS: readonly string[] = ["kubernetes", "flux", "alertmanager"];

/** plugins.<id>.actions.agents, in the order the choice offers them. */
const POLICIES = [
  { value: "approve", label: "Approve", note: "each call waits for you to approve it here" },
  { value: "allow", label: "Allow", note: "runs at once, except on protected targets" },
  { value: "deny", label: "Deny", note: "agents get none of the plugin's action tools" },
] as const;

/** The policy dash applies when the setting is unset. */
const DEFAULT_POLICY = "approve";

/**
 * Settings → MCP's agent policy (design O.1, D48): for each plugin with agent action tools that
 * this dash has, plugins.<id>.actions.agents, written through PUT /settings/plugins/{id} with the
 * revision of the plugin's last read. It applies live. A value the environment or the config file
 * sets, or a read-only store, locks the choice and says how to change it.
 */
export function AgentPolicyPanel({ view }: { view: SettingsView }): JSX.Element | null {
  const plugins = view.plugins.filter((p) => AGENT_POLICY_PLUGINS.includes(p.id));
  if (plugins.length === 0) return null;
  return (
    <Panel>
      <PanelHeader title="Agent actions" icon={<ShieldCheck />} />
      <div className="space-y-4 px-4 pb-4">
        <p className="text-sm text-muted">
          What an AI agent's call of a plugin action through MCP does. Approve asks you: the call
          waits until you approve or deny it in dash. Allow runs it directly, except on protected
          targets, which always ask. Deny hides the plugin's action tools from agents. API tokens
          and signed-in people are never affected, and a change applies at once.
        </p>
        {plugins.map((p) => (
          <AgentPolicyRow key={p.id} plugin={p} readonly={view.store.readonly} />
        ))}
      </div>
    </Panel>
  );
}

function AgentPolicyRow({
  plugin,
  readonly,
}: {
  plugin: SettingsPlugin;
  readonly: boolean;
}): JSX.Element | null {
  const queryClient = useQueryClient();
  const settings = useSettingsPlugin(plugin.id);
  const [pending, setPending] = useState(false);
  const lockId = useId();
  if (settings.isPending) return <Skeleton className="h-20 w-full" />;
  if (settings.isError) {
    return (
      <p className="text-xs text-crit">
        {plugin.name}: the settings could not be read: {errorMessage(settings.error)}
      </p>
    );
  }
  const base = `plugins.${plugin.id}.actions.`;
  const field = settings.data.fields.find((f) => f.path === `${base}agents`);
  // A dash that predates the policy has no such field: nothing to choose.
  if (!field) return null;
  const actionsOn = settings.data.fields.find((f) => f.path === `${base}enabled`)?.value === true;
  const value = typeof field.value === "string" ? field.value : DEFAULT_POLICY;
  const lock = liveLockText(field, readonly, "change");
  const revision = settings.data.revision;

  async function choose(agents: string) {
    setPending(true);
    try {
      await api.put(
        settingsPaths.plugin(plugin.id),
        { actions: { agents } },
        { ifMatch: revision },
      );
      toast.success(`${plugin.name}: agent actions set to ${agents}`);
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      // After a failure too: a 412 leaves the next attempt the fresh revision.
      await invalidateSettings(queryClient);
      setPending(false);
    }
  }

  return (
    <fieldset
      className="space-y-1.5"
      disabled={lock !== undefined || pending}
      aria-describedby={lock ? lockId : undefined}
    >
      <legend className="mb-1 flex flex-wrap items-baseline gap-2 text-sm font-medium text-fg">
        {plugin.name}
        {lock ? null : <span className="text-xs font-normal text-faint">{sourceText(field)}</span>}
      </legend>
      <div className="flex flex-wrap gap-x-5 gap-y-1">
        {POLICIES.map((p) => (
          <label
            key={p.value}
            className="flex cursor-pointer items-center gap-2 text-sm has-disabled:cursor-not-allowed has-disabled:opacity-60"
          >
            <input
              type="radio"
              name={`agents-${plugin.id}`}
              value={p.value}
              checked={value === p.value}
              onChange={() => void choose(p.value)}
              className="accent-accent"
            />
            <span className="text-fg">{p.label}</span>
            <span className="text-xs text-muted">{p.note}</span>
          </label>
        ))}
      </div>
      {lock ? (
        <p id={lockId} className="text-xs text-muted">
          {lock}
        </p>
      ) : null}
      {actionsOn ? null : (
        <p className="text-xs text-faint">
          The plugin's actions are off ({base}enabled), so agents have no action tools for it yet.
        </p>
      )}
    </fieldset>
  );
}
