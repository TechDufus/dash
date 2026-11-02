import { GitMerge, ShieldAlert, ShieldCheck } from "lucide-react";
import type { Health } from "@/api/types";
import { Kpi } from "@/components/kpi";
import { cn } from "@/lib/utils";
import { KPI_TILE, KpiLink } from "../../kpi-link";
import type { ModulePanels, SlotProps } from "../../types";

/**
 * The plugin's one tile. It owns the presentation of every KPI the plugin publishes (the overview
 * draws no generic tile for a plugin that contributes one). `verified` is published only when a
 * source is pinned or signed: absent means there is nothing to verify, 0 means one of them is not.
 */
function GitOpsTile({ data }: SlotProps["overview.kpis"]) {
  const kpi = (key: string) =>
    data.kpiList.find((x) => x.plugin === "flux" && x.key === key)?.value;
  const ready = kpi("ready");
  const total = kpi("total");
  if (ready === undefined || total === undefined) return null;
  const suspended = kpi("suspended") ?? 0;
  const verified = kpi("verified");
  const unverified = verified === 0;
  const gitops: Health = ready < total || unverified ? "warn" : "ok";
  const note = [
    verified === undefined ? "" : unverified ? "Source not verified" : "Source verified",
    suspended > 0 ? `${suspended} suspended` : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <KpiLink href="/gitops" label={`GitOps ${ready} of ${total} ready. Open GitOps`}>
      <Kpi
        label="GitOps"
        value={ready}
        suffix={`/ ${total}`}
        health={gitops}
        icon={
          verified === undefined ? (
            <GitMerge aria-hidden="true" />
          ) : unverified ? (
            <ShieldAlert className="text-warn" role="img" aria-label="Source not verified" />
          ) : (
            <ShieldCheck className="text-ok" role="img" aria-label="Source verified" />
          )
        }
        spark={
          <p
            className={cn(
              "flex h-full items-end px-1 text-xs",
              !unverified && suspended === 0 ? "text-muted" : "text-warn",
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
