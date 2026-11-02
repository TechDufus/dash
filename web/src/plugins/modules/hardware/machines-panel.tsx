import { Server } from "lucide-react";
import { useTopic } from "@/api/stream";
import { Panel, PanelHeader } from "@/components/panel";
import { HealthBadge } from "@/components/status";

/**
 * The machines dash watches that are neither VMs nor cluster nodes, from hardware.machines. It
 * draws nothing until the topic has arrived and nothing when no machine is configured: the
 * plugin publishes `[]` then, and an empty panel would only say "none" on a page about the host.
 */
export function MachinesPanel({ title = "Other machines" }: { title?: string }) {
  const env = useTopic("hardware.machines");
  const machines = env?.data.machines ?? [];
  if (machines.length === 0) return null;
  return (
    <Panel>
      <PanelHeader title={title} icon={<Server />} sources={env?.sources} />
      <ul className="grid gap-3 px-4 pb-4 sm:grid-cols-2 xl:grid-cols-4">
        {machines.map((m) => (
          <li
            key={m.ref}
            className="flex items-center justify-between gap-3 rounded-xl border border-line bg-panel px-3 py-2.5"
          >
            <div className="min-w-0">
              <div className="truncate text-sm font-medium text-fg">{m.name}</div>
              <div className="num truncate text-xs text-muted" title={m.source}>
                {m.source}
              </div>
            </div>
            <HealthBadge health={m.up ? "ok" : "crit"} label={m.up ? "Up" : "Down"} />
          </li>
        ))}
      </ul>
    </Panel>
  );
}
