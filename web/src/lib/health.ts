import type { Health } from "@/api/types";

const ORDER: Record<Health, number> = { ok: 0, unknown: 1, warn: 2, crit: 3 };

/** Worst of the given healths (ok when empty). */
export function worstHealth(items: Iterable<Health>): Health {
  let worst: Health = "ok";
  for (const h of items) if (ORDER[h] > ORDER[worst]) worst = h;
  return worst;
}

export const HEALTH_LABEL: Record<Health, string> = {
  ok: "OK",
  warn: "Warning",
  crit: "Critical",
  unknown: "Unknown",
};

/** Tailwind text colour class per health. */
export const HEALTH_TEXT: Record<Health, string> = {
  ok: "text-ok",
  warn: "text-warn",
  crit: "text-crit",
  unknown: "text-faint",
};

export const HEALTH_BG: Record<Health, string> = {
  ok: "bg-ok",
  warn: "bg-warn",
  crit: "bg-crit",
  unknown: "bg-faint",
};

/** CSS colour (usable in SVG attributes) per health. */
export const HEALTH_COLOR: Record<Health, string> = {
  ok: "var(--color-ok)",
  warn: "var(--color-warn)",
  crit: "var(--color-crit)",
  unknown: "var(--color-faint)",
};

/** Health from a used ratio with the plan's 0.80 / 0.90 thresholds. */
export function ratioHealth(ratio: number | undefined | null): Health {
  if (ratio == null || !Number.isFinite(ratio)) return "unknown";
  if (ratio >= 0.9) return "crit";
  if (ratio >= 0.8) return "warn";
  return "ok";
}
