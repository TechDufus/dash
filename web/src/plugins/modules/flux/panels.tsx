import { ShieldAlert, ShieldCheck } from "lucide-react";
import type { Health } from "@/api/types";
import { Kpi } from "@/components/kpi";
import { cn } from "@/lib/utils";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";

function GitOpsTile({ data }: SlotProps["overview.kpis"]) {
  const k = data.kpis;
  const gitops: Health = k.fluxReady < k.fluxTotal || !k.sourceVerified ? "warn" : "ok";
  const note = [
    k.sourceVerified ? "Source verified" : "Source not verified",
    k.fluxSuspended > 0 ? `${k.fluxSuspended} suspended` : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <KpiLink href="/gitops" label={`GitOps ${k.fluxReady} of ${k.fluxTotal} ready. Open GitOps`}>
      <Kpi
        label="GitOps"
        value={k.fluxReady}
        suffix={`/ ${k.fluxTotal}`}
        health={gitops}
        icon={
          k.sourceVerified ? (
            <ShieldCheck className="text-ok" role="img" aria-label="Source verified" />
          ) : (
            <ShieldAlert className="text-warn" role="img" aria-label="Source not verified" />
          )
        }
        spark={
          <p
            className={cn(
              "flex h-full items-end px-1 text-xs",
              k.sourceVerified && k.fluxSuspended === 0 ? "text-muted" : "text-warn",
            )}
          >
            {note}
          </p>
        }
        className={KPI_TILE}
      />
    </KpiLink>
  );
}

export const panels: ModulePanels = {
  "overview.kpis": [{ id: "gitops", order: 20, reads: ["core.overview"], component: GitOpsTile }],
};
