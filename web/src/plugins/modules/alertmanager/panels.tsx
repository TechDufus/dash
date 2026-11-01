import { BellRing } from "lucide-react";
import { Kpi } from "@/components/kpi";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";

/**
 * The plugin's one tile. It owns the presentation of every KPI the plugin publishes (the
 * overview draws no generic tile for a plugin that contributes one), and takes the health from
 * the KPI itself: crit when a firing alert is critical, warn when anything fires.
 */
function AlertsFiringTile({ data }: SlotProps["overview.kpis"]) {
  const k = data.kpiList.find((x) => x.plugin === "alertmanager" && x.key === "firing");
  if (!k) return null;
  return (
    <KpiLink href="/alerts" label={`Alerts firing: ${k.value}. Open alerts`}>
      <Kpi
        label="Alerts firing"
        value={k.value}
        health={k.health}
        icon={<BellRing aria-hidden="true" />}
        className={KPI_TILE}
      />
    </KpiLink>
  );
}

export const panels: ModulePanels = {
  "overview.kpis": [
    { id: "alerts-firing", order: 10, reads: ["core.overview"], component: AlertsFiringTile },
  ],
};
