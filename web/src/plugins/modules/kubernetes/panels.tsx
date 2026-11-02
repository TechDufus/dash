import { Boxes } from "lucide-react";
import { Kpi } from "@/components/kpi";
import { AppsSection } from "@/components/launcher";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";
import { BackupEtcdPanel, PvcPanel } from "./storage-panels";

/**
 * The plugin's one tile. It owns the presentation of every KPI the plugin publishes (the overview
 * draws no generic tile for a plugin that contributes one) and takes its health from the
 * ready-nodes KPI. The worst volume (`pvc.worst`) is the overview page's own line.
 */
function NodesTile({ data }: SlotProps["overview.kpis"]) {
  const ready = data.kpiList.find((x) => x.plugin === "kubernetes" && x.key === "nodes.ready");
  const total = data.kpiList.find((x) => x.plugin === "kubernetes" && x.key === "nodes.total");
  if (!ready || !total) return null;
  return (
    <KpiLink href="/cluster" label={`Nodes ${ready.value} of ${total.value} ready. Open cluster`}>
      <Kpi
        label="Nodes"
        value={ready.value}
        suffix={`/ ${total.value}`}
        health={ready.health}
        icon={<Boxes aria-hidden="true" />}
        className={KPI_TILE}
      />
    </KpiLink>
  );
}

export const panels: ModulePanels = {
  "overview.kpis": [{ id: "nodes", order: 30, reads: ["core.overview"], component: NodesTile }],
  storage: [
    { id: "pvcs", order: 10, span: "full", reads: ["kubernetes.storage"], component: PvcPanel },
    { id: "backups-etcd", order: 50, reads: ["kubernetes.storage"], component: BackupEtcdPanel },
  ],
  "apps.sources": [{ id: "apps", order: 10, reads: ["kubernetes.apps"], component: AppsSection }],
};
