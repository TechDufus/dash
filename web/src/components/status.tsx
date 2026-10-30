import { CircleCheck, CircleHelp, OctagonAlert, TriangleAlert } from "lucide-react";
import type { Health } from "@/api/types";
import { HEALTH_BG, HEALTH_LABEL, HEALTH_TEXT } from "@/lib/health";
import { cn } from "@/lib/utils";

/** 8 px dot in the health colour; warn and crit add a 2 s ping ring. */
export function StatusDot({ health, className }: { health: Health; className?: string }) {
  const ping = health === "warn" || health === "crit";
  return (
    <span
      role="img"
      aria-label={HEALTH_LABEL[health]}
      className={cn("relative inline-flex size-2 shrink-0", className)}
    >
      {ping && (
        <span
          className={cn(
            "animate-dash-ping absolute inline-flex size-full rounded-full opacity-60",
            HEALTH_BG[health],
          )}
        />
      )}
      <span className={cn("relative inline-flex size-2 rounded-full", HEALTH_BG[health])} />
    </span>
  );
}

const ICON = {
  ok: CircleCheck,
  warn: TriangleAlert,
  crit: OctagonAlert,
  unknown: CircleHelp,
} as const;

interface HealthBadgeProps {
  health: Health;
  /** Overrides the default label ("OK", "Warning", "Critical", "Unknown"). */
  label?: string;
  className?: string;
}

/** Icon plus text, so status is never conveyed by colour alone. */
export function HealthBadge({ health, label, className }: HealthBadgeProps) {
  const Icon = ICON[health];
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium",
        health === "ok" && "border-ok/25 bg-ok/10",
        health === "warn" && "border-warn/30 bg-warn/10",
        health === "crit" && "border-crit/30 bg-crit/10",
        health === "unknown" && "border-line bg-panel",
        HEALTH_TEXT[health],
        className,
      )}
    >
      <Icon className="size-3" aria-hidden="true" />
      <span data-slot="label">{label ?? HEALTH_LABEL[health]}</span>
    </span>
  );
}
