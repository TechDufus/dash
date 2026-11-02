import { Flame, Zap } from "lucide-react";
import { Kpi } from "@/components/kpi";
import { SparkSeries } from "@/components/spark";
import { formatCelsius, formatWatts } from "@/lib/format";
import { HEALTH_COLOR } from "@/lib/health";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";

// Both tiles read the plugin's own KPIs, which exist only after the host has polled once: a tile
// draws nothing until then.
function PowerTile({ data }: SlotProps["overview.kpis"]) {
  const power = data.kpiList.find((x) => x.plugin === "hardware" && x.key === "power");
  if (!power) return null;
  return (
    <KpiLink href="/hardware" label={`Power ${formatWatts(power.value)}. Open hardware`}>
      <Kpi
        label="Power"
        value={power.value}
        unit="W"
        health={power.health}
        icon={<Zap aria-hidden="true" />}
        spark={<SparkSeries name="host.power" range="6h" format={formatWatts} />}
        className={KPI_TILE}
      />
    </KpiLink>
  );
}

function CpuTempTile({ data }: SlotProps["overview.kpis"]) {
  const temp = data.kpiList.find((x) => x.plugin === "hardware" && x.key === "cpuTemp");
  if (!temp) return null;
  return (
    <KpiLink href="/hardware" label={`CPU temperature ${formatCelsius(temp.value)}. Open hardware`}>
      <Kpi
        label="CPU temp"
        value={temp.value}
        unit="°C"
        health={temp.health}
        icon={<Flame aria-hidden="true" />}
        spark={
          <SparkSeries
            name="host.cpuTemp"
            range="6h"
            color={temp.health === "ok" ? undefined : HEALTH_COLOR[temp.health]}
            format={formatCelsius}
          />
        }
        className={KPI_TILE}
      />
    </KpiLink>
  );
}

export const panels: ModulePanels = {
  "overview.kpis": [
    { id: "power", order: 50, reads: ["core.overview"], component: PowerTile },
    { id: "cpu-temp", order: 60, reads: ["core.overview"], component: CpuTempTile },
  ],
};
