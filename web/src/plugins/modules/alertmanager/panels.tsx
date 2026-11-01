import { BellRing } from "lucide-react";
import type { Health, OverviewData } from "@/api/types";
import { Kpi } from "@/components/kpi";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";

function alertsHealth(data: OverviewData): Health {
  if (data.attention.some((a) => a.severity === "crit")) return "crit";
  if (data.kpis.alertsFiring > 0 || data.attention.some((a) => a.severity === "warn"))
    return "warn";
  return "ok";
}

function AlertsFiringTile({ data }: SlotProps["overview.kpis"]) {
  const k = data.kpis;
  return (
    <KpiLink href="/alerts" label={`Alerts firing: ${k.alertsFiring}. Open alerts`}>
      <Kpi
        label="Alerts firing"
        value={k.alertsFiring}
        health={alertsHealth(data)}
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
