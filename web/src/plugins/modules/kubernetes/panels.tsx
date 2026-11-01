import { Boxes } from "lucide-react";
import type { Health } from "@/api/types";
import { Kpi } from "@/components/kpi";
import { AppsSection } from "@/components/launcher";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";
import { CnpgPanel, NasEtcdPanel, ObjectStoragePanel, PvcPanel } from "./storage-panels";

function NodesTile({ data }: SlotProps["overview.kpis"]) {
  const k = data.kpis;
  const nodes: Health = k.nodesReady < k.nodesTotal ? "crit" : "ok";
  return (
    <KpiLink href="/cluster" label={`Nodes ${k.nodesReady} of ${k.nodesTotal} ready. Open cluster`}>
      <Kpi
        label="Nodes"
        value={k.nodesReady}
        suffix={`/ ${k.nodesTotal}`}
        health={nodes}
        icon={<Boxes aria-hidden="true" />}
        className={KPI_TILE}
      />
    </KpiLink>
  );
}

export const panels: ModulePanels = {
  "overview.kpis": [{ id: "nodes", order: 30, reads: ["core.overview"], component: NodesTile }],
  storage: [
    { id: "pvcs", order: 10, span: "full", reads: ["storage"], component: PvcPanel },
    { id: "object-storage", order: 20, reads: ["storage"], component: ObjectStoragePanel },
    { id: "cnpg", order: 40, reads: ["storage"], component: CnpgPanel },
    { id: "nas-etcd", order: 50, reads: ["storage"], component: NasEtcdPanel },
  ],
  "apps.sources": [{ id: "apps", order: 10, reads: ["apps"], component: AppsSection }],
};
