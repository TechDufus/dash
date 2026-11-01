import { Monitor, Server } from "lucide-react";
import type { Health } from "@/api/types";
import { BytesBar } from "@/components/bytes-bar";
import { Kpi } from "@/components/kpi";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";

function VmsTile({ data }: SlotProps["overview.kpis"]) {
  const k = data.kpis;
  const vms: Health = k.vmsRunning < k.vmsTotal ? "warn" : "ok";
  return (
    <KpiLink href="/vms" label={`VMs ${k.vmsRunning} of ${k.vmsTotal} running. Open VMs`}>
      <Kpi
        label="VMs"
        value={k.vmsRunning}
        suffix={`/ ${k.vmsTotal}`}
        health={vms}
        icon={<Monitor aria-hidden="true" />}
        className={KPI_TILE}
      />
    </KpiLink>
  );
}

function ProxmoxStoragePanel({ env }: SlotProps["storage"]) {
  if (!env) return <PanelSkeleton lines={3} />;
  return (
    <Panel className="h-full">
      <PanelHeader title="Proxmox storage" icon={<Server />} />
      <div className="space-y-4 px-4 pb-4">
        {env.data.pveStorages.length === 0 && <Empty className="py-4">No storages.</Empty>}
        {env.data.pveStorages.map((s) => (
          <BytesBar key={s.name} used={s.usedBytes} total={s.sizeBytes} label={s.name} />
        ))}
      </div>
    </Panel>
  );
}

export const panels: ModulePanels = {
  "overview.kpis": [{ id: "vms", order: 40, reads: ["core.overview"], component: VmsTile }],
  // The Proxmox storages arrive in the kubernetes plugin's storage topic (until a proxmox topic carries them).
  storage: [
    { id: "proxmox-storage", order: 30, reads: ["storage"], component: ProxmoxStoragePanel },
  ],
};
