import { useReducedMotion } from "motion/react";
import type { ReactNode } from "react";
import type { Health } from "@/api/types";
import { NumberTicker } from "@/components/magicui/number-ticker";
import { Panel } from "@/components/panel";
import { StatusDot } from "@/components/status";
import { cn } from "@/lib/utils";

interface KpiProps {
  label: string;
  value: number | undefined;
  decimals?: number;
  unit?: string;
  /** Text after the value, e.g. "/ 35". */
  suffix?: ReactNode;
  health?: Health;
  /** Sparkline or other trailing visual. */
  spark?: ReactNode;
  icon?: ReactNode;
  className?: string;
}

/** KPI tile: label, animated number, unit, optional spark. */
export function Kpi({
  label,
  value,
  decimals = 0,
  unit,
  suffix,
  health,
  spark,
  icon,
  className,
}: KpiProps) {
  const reduced = useReducedMotion();
  return (
    <Panel className={cn("flex min-h-[118px] flex-col justify-between gap-2 p-4", className)}>
      <div className="flex items-center gap-2 text-xs font-medium tracking-wide text-muted uppercase">
        {icon && <span className="[&_svg]:size-3.5">{icon}</span>}
        <span className="truncate">{label}</span>
        {health && <StatusDot health={health} className="ml-auto" />}
      </div>
      <div className="flex items-baseline gap-1.5">
        <span className="num text-3xl font-semibold text-fg">
          {value === undefined ? (
            "–"
          ) : reduced ? (
            value.toLocaleString("en-US", {
              minimumFractionDigits: decimals,
              maximumFractionDigits: decimals,
            })
          ) : (
            <NumberTicker value={value} decimalPlaces={decimals} className="text-fg" />
          )}
        </span>
        {suffix && <span className="num text-base text-muted">{suffix}</span>}
        {unit && <span className="text-sm text-muted">{unit}</span>}
      </div>
      <div className="-mx-1 -mb-1 h-10">{spark}</div>
    </Panel>
  );
}
