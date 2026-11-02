import { Database } from "lucide-react";
import { useTopic } from "@/api/stream";
import type { CNPGCluster, Health } from "@/api/types";
import { BackupAge } from "@/components/backup-age";
import { HeadlampIcon } from "@/components/links";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { StatusDot } from "@/components/status";
import { HEALTH_TEXT } from "@/lib/health";
import { cn } from "@/lib/utils";
import type { ModulePanels, SlotProps } from "../../types";

function instancesHealth(c: CNPGCluster): Health {
  if (c.instances > 0 && c.readyInstances >= c.instances) return "ok";
  return c.readyInstances > 0 ? "warn" : "crit";
}

function CnpgRow({ c }: { c: CNPGCluster }) {
  const ih = instancesHealth(c);
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 py-3">
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm text-fg">
          <span className="text-muted">{c.namespace}/</span>
          {c.name}
        </div>
        <div className="text-xs text-muted">{c.phase || "–"}</div>
      </div>
      <span className="inline-flex items-center gap-1.5">
        <StatusDot health={ih} />
        <span className={cn("num text-sm font-medium", HEALTH_TEXT[ih])}>
          {c.readyInstances}/{c.instances}
        </span>
        <span className="text-xs text-muted">ready</span>
      </span>
      <div className="flex min-w-[150px] flex-col">
        <span className="text-[11px] text-muted">Last backup</span>
        <BackupAge seconds={c.lastBackupAgeSeconds} />
      </div>
      <HeadlampIcon links={c.links} />
    </div>
  );
}

/** Reads its own topic and draws a skeleton, not an empty list, until it arrives. */
export function CnpgPanel(_: SlotProps["storage"]) {
  const env = useTopic("cnpg.clusters");
  if (!env) return <PanelSkeleton lines={3} />;
  const { clusters } = env.data;
  return (
    <Panel className="h-full">
      <PanelHeader title="Postgres clusters (CNPG)" icon={<Database />} />
      <div className="divide-y divide-line px-4 pb-2">
        {clusters.length === 0 && <Empty className="py-4">No CNPG clusters.</Empty>}
        {clusters.map((c) => (
          <CnpgRow key={`${c.namespace}/${c.name}`} c={c} />
        ))}
      </div>
    </Panel>
  );
}

export const panels: ModulePanels = {
  storage: [{ id: "cnpg", order: 40, reads: ["cnpg.clusters"], component: CnpgPanel }],
};
