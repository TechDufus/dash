import { Monitor, Server } from "lucide-react";
import { useTopic } from "@/api/stream";
import { BytesBar } from "@/components/bytes-bar";
import { Kpi } from "@/components/kpi";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { formatDuration } from "@/lib/format";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";

/**
 * The plugin's one tile. It owns the presentation of every KPI the plugin publishes (the overview
 * draws no generic tile for a plugin that contributes one), and takes the health from the
 * running-VMs KPI itself rather than recomputing it.
 */
function VmsTile({ data }: SlotProps["overview.kpis"]) {
  const running = data.kpiList.find((x) => x.plugin === "proxmox" && x.key === "vms.running");
  const total = data.kpiList.find((x) => x.plugin === "proxmox" && x.key === "vms.total");
  if (!running || !total) return null;
  return (
    <KpiLink href="/vms" label={`VMs ${running.value} of ${total.value} running. Open VMs`}>
      <Kpi
        label="VMs"
        value={running.value}
        suffix={`/ ${total.value}`}
        health={running.health}
        icon={<Monitor aria-hidden="true" />}
        className={KPI_TILE}
      />
    </KpiLink>
  );
}

/** Reads its own topic, so the storage page needs no kubernetes envelope to draw it. */
function ProxmoxStoragePanel(_: SlotProps["storage"]) {
  const env = useTopic("proxmox.storage");
  if (!env) return <PanelSkeleton lines={3} />;
  const { storages, usedBytes, sizeBytes } = env.data;
  return (
    <Panel className="h-full">
      <PanelHeader title="Proxmox storage" icon={<Server />} />
      <div className="space-y-4 px-4 pb-4">
        {storages.length === 0 && <Empty className="py-4">No storages.</Empty>}
        {storages.length > 0 && (
          // Each shared storage counts once here, however many nodes reach it.
          <BytesBar used={usedBytes} total={sizeBytes} label="Total" />
        )}
        {storages.map((s) => (
          <BytesBar
            key={s.ref}
            used={s.usedBytes}
            total={s.sizeBytes}
            label={`${s.name} · ${s.shared ? "shared" : s.node}`}
          />
        ))}
      </div>
    </Panel>
  );
}

function Fact({ label, value, title }: { label: string; value: string; title?: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted">{label}</dt>
      <dd className="num truncate text-sm text-fg" title={title ?? value}>
        {value}
      </dd>
    </div>
  );
}

/**
 * The facts of the Proxmox node the Hardware page's host is, drawn inside its System panel. A host
 * that is no node of the cluster gets none: another node's facts would describe another machine.
 */
function NodeFacts({ host }: SlotProps["hardware.facts"]) {
  const env = useTopic("proxmox.cluster");
  const node = host?.node ? env?.data.nodes.find((n) => n.node === host.node) : undefined;
  if (!node) return null;
  return (
    <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
      <Fact
        label="Uptime"
        value={node.status === "online" ? formatDuration(node.uptimeSeconds) : node.status}
      />
      <Fact label="Proxmox VE" value={node.version || "–"} />
      <Fact label="Kernel" value={node.kernel || "–"} />
      <Fact label="CPU" value={node.cpuModel || "–"} />
    </dl>
  );
}

export const panels: ModulePanels = {
  "overview.kpis": [{ id: "vms", order: 40, reads: ["core.overview"], component: VmsTile }],
  storage: [
    {
      id: "proxmox-storage",
      order: 30,
      reads: ["proxmox.storage"],
      component: ProxmoxStoragePanel,
    },
  ],
  "hardware.facts": [
    { id: "proxmox-node", order: 10, reads: ["proxmox.cluster"], component: NodeFacts },
  ],
};
