import { TriangleAlert } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { formatBytes, formatPercent } from "@/lib/format";
import { HEALTH_BG, HEALTH_TEXT, ratioHealth } from "@/lib/health";
import { cn } from "@/lib/utils";

/** Tooltip for VM memory figures, which Proxmox reports including the guest's page cache. */
export const PVE_MEM_TIP = "As Proxmox sees it: includes the guest's page cache";

interface BytesBarProps {
  used: number | undefined;
  total: number | undefined;
  /** Text left of the numbers. */
  label?: string;
  /** Hide the "used / total" text (compact bars). */
  compact?: boolean;
  /** Shown instead of the total when there is none (e.g. "no quota"). */
  noTotalLabel?: string;
  /** Keep the label for screen readers only (the surrounding row already names the item). */
  labelHidden?: boolean;
  /** Always accent colour: no warn/crit colouring (for figures that are not real capacity). */
  neutral?: boolean;
  /** Tooltip on the label explaining what the figure means. */
  labelTip?: string;
  className?: string;
}

/** Used/total bar; warn at 80 %, crit at 90 % unless `neutral`. */
export function BytesBar({
  used,
  total,
  label,
  compact,
  noTotalLabel,
  labelHidden,
  neutral,
  labelTip,
  className,
}: BytesBarProps) {
  const hasTotal = total != null && total > 0;
  const ratio = hasTotal && used != null ? used / total : undefined;
  const health = neutral ? "ok" : ratioHealth(ratio);
  const pct = ratio == null ? 0 : Math.min(1, ratio);
  const labelNode = (
    <span
      className={cn(
        "shrink-0 text-muted",
        labelHidden && "sr-only",
        labelTip && "cursor-help underline decoration-dotted underline-offset-2",
      )}
    >
      {label}
    </span>
  );
  return (
    <div className={cn("w-full", className)}>
      {!compact && (
        <div className="mb-1 flex flex-wrap items-baseline justify-between gap-x-2 text-xs">
          {labelTip ? (
            <Tooltip>
              <TooltipTrigger asChild>{labelNode}</TooltipTrigger>
              <TooltipContent>{labelTip}</TooltipContent>
            </Tooltip>
          ) : (
            labelNode
          )}
          <span className="num ml-auto shrink-0 text-fg">
            {formatBytes(used)}
            {hasTotal ? ` / ${formatBytes(total)}` : noTotalLabel ? ` · ${noTotalLabel}` : ""}
            {ratio != null && (
              <span
                className={cn(
                  "ml-1.5 inline-flex items-center gap-0.5",
                  neutral ? "text-muted" : HEALTH_TEXT[health],
                )}
              >
                {health !== "ok" && <TriangleAlert className="size-3" aria-hidden="true" />}
                {formatPercent(ratio)}
              </span>
            )}
          </span>
        </div>
      )}
      <div
        className="h-1.5 w-full overflow-hidden rounded-full bg-line"
        role="progressbar"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(pct * 100)}
        aria-label={label ?? "usage"}
      >
        <div
          className={cn(
            "h-full rounded-full transition-[width] duration-500",
            health === "unknown"
              ? "bg-accent/60"
              : health === "ok"
                ? "bg-accent"
                : HEALTH_BG[health],
          )}
          style={{ width: `${pct * 100}%` }}
        />
      </div>
    </div>
  );
}
