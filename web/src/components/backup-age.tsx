import type { Health } from "@/api/types";
import { HealthBadge } from "@/components/status";
import { formatDuration } from "@/lib/format";
import { HEALTH_TEXT } from "@/lib/health";
import { cn } from "@/lib/utils";

const HOUR = 3600;

/** Backup freshness: warn after 36 h, crit after 72 h; unknown without a recorded success. */
export function backupHealth(ageSeconds: number | undefined): Health {
  if (ageSeconds === undefined) return "unknown";
  if (ageSeconds > 72 * HOUR) return "crit";
  if (ageSeconds > 36 * HOUR) return "warn";
  return "ok";
}

/** How long ago a backup last succeeded, with its freshness badge. */
export function BackupAge({ seconds }: { seconds: number | undefined }) {
  const h = backupHealth(seconds);
  return h === "unknown" ? (
    <span className="text-xs text-muted">No success recorded</span>
  ) : (
    <span className="inline-flex items-center gap-2">
      <span className={cn("num text-sm font-medium", HEALTH_TEXT[h])}>
        {formatDuration(seconds)} ago
      </span>
      <HealthBadge
        health={h}
        label={h === "ok" ? "Fresh" : h === "warn" ? "Stale >36 h" : "Stale >72 h"}
      />
    </span>
  );
}
