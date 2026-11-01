import { Flame, Zap } from "lucide-react";
import { Kpi } from "@/components/kpi";
import { SparkSeries } from "@/components/spark";
import { formatCelsius, formatWatts } from "@/lib/format";
import { HEALTH_COLOR } from "@/lib/health";
import { tempHealth } from "@/pages/topology";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";

function PowerTile({ data }: SlotProps["overview.kpis"]) {
  const k = data.kpis;
  return (
    <KpiLink href="/hardware" label={`Power ${formatWatts(k.powerWatts)}. Open hardware`}>
      <Kpi
        label="Power"
        value={k.powerWatts}
        unit="W"
        icon={<Zap aria-hidden="true" />}
        spark={<SparkSeries name="host.power" range="6h" format={formatWatts} />}
        className={KPI_TILE}
      />
    </KpiLink>
  );
}

function CpuTempTile({ data }: SlotProps["overview.kpis"]) {
  const k = data.kpis;
  const temp = tempHealth(k.cpuTempMaxC);
  return (
    <KpiLink
      href="/hardware"
      label={`CPU temperature ${formatCelsius(k.cpuTempMaxC)}. Open hardware`}
    >
      <Kpi
        label="CPU temp"
        value={k.cpuTempMaxC}
        unit="°C"
        health={temp}
        icon={<Flame aria-hidden="true" />}
        spark={
          <SparkSeries
            name="host.cpuTemp"
            range="6h"
            color={temp === "ok" ? undefined : HEALTH_COLOR[temp]}
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
