import { TriangleAlert } from "lucide-react";
import { formatBytes, formatPercent } from "@/lib/format";
import { HEALTH_BG, HEALTH_TEXT, ratioHealth } from "@/lib/health";
import { cn } from "@/lib/utils";

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
  className?: string;
}

/** Used/total bar; warn at 80 %, crit at 90 %. */
export function BytesBar({
  used,
  total,
  label,
  compact,
  noTotalLabel,
  labelHidden,
  className,
}: BytesBarProps) {
  const hasTotal = total != null && total > 0;
  const ratio = hasTotal && used != null ? used / total : undefined;
  const health = ratioHealth(ratio);
  const pct = ratio == null ? 0 : Math.min(1, ratio);
  return (
    <div className={cn("w-full", className)}>
      {!compact && (
        <div className="mb-1 flex flex-wrap items-baseline justify-between gap-x-2 text-xs">
          <span className={cn("shrink-0 text-muted", labelHidden && "sr-only")}>{label}</span>
          <span className="num ml-auto shrink-0 text-fg">
            {formatBytes(used)}
            {hasTotal ? ` / ${formatBytes(total)}` : noTotalLabel ? ` · ${noTotalLabel}` : ""}
            {ratio != null && (
              <span className={cn("ml-1.5 inline-flex items-center gap-0.5", HEALTH_TEXT[health])}>
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
