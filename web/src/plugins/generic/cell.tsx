import type { ReactNode } from "react";
import type { Health } from "@/api/types";
import { StatusDot } from "@/components/status";
import { formatBytes, formatNumber, formatPercent, formatRelative } from "@/lib/format";

const HEALTHS: readonly string[] = ["ok", "warn", "crit"];
const DASH = "–";

/** A cell value as plain text, in the way its column format says. Never throws. */
export function formatCell(value: unknown, format: string | undefined): string {
  if (value === null || value === undefined) return DASH;
  switch (format) {
    case "number":
      return typeof value === "number"
        ? formatNumber(value, Number.isInteger(value) ? 0 : 2)
        : DASH;
    case "bytes":
      return typeof value === "number" ? formatBytes(value) : DASH;
    case "percent":
      return typeof value === "number" ? formatPercent(value) : DASH;
    case "time":
      return typeof value === "string" || typeof value === "number" ? formatRelative(value) : DASH;
    default:
      return typeof value === "object" ? DASH : String(value);
  }
}

/** The health a `health` cell shows: ok, warn and crit are known, anything else is unknown. */
export function cellHealth(value: unknown): Health {
  return typeof value === "string" && HEALTHS.includes(value) ? (value as Health) : "unknown";
}

/** A cell value as a node: `health` draws a status dot, every other format is text. */
export function Cell({ value, format }: { value: unknown; format: string | undefined }): ReactNode {
  if (format === "health") return <StatusDot health={cellHealth(value)} />;
  return formatCell(value, format);
}
