import type { Health } from "@/api/types";

/** Rows rendered before the "show all" button (keeps big tables fast). */
export const ROW_CAP = 200;

const HEALTH_SORT: Record<Health, number> = { crit: 0, warn: 1, unknown: 2, ok: 3 };

/** Orders worst health first. */
export function compareHealth(a: Health, b: Health): number {
  return HEALTH_SORT[a] - HEALTH_SORT[b];
}

/** CPU cores as millicores below one core ("12m"), else cores with two decimals ("1.20"). */
export function formatCores(cores: number | undefined | null): string {
  if (cores == null || !Number.isFinite(cores)) return "–";
  if (cores < 1) return `${Math.round(cores * 1000)}m`;
  return cores.toFixed(2);
}

/** Case-insensitive substring match of an already-lowercased needle against any field. */
export function matchesQuery(needle: string, ...fields: (string | undefined)[]): boolean {
  if (needle === "") return true;
  return fields.some((f) => f?.toLowerCase().includes(needle));
}

/** The pod sheet route. */
export function podPath(namespace: string, name: string): string {
  return `/cluster/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}`;
}

/** Filter params carried between the Pods tab and the pod sheet so closing it restores them. */
export const FILTER_KEYS = ["ns", "q", "health"] as const;

export function filterSearch(params: URLSearchParams): string {
  const n = new URLSearchParams();
  for (const k of FILTER_KEYS) {
    const v = params.get(k);
    if (v) n.set(k, v);
  }
  return n.toString();
}
