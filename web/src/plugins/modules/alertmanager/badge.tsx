import { useTopic } from "@/api/stream";

/** The firing-alert count, drawn on the Alerts rail entry. */
export function AlertsBadge() {
  const firing = useTopic("core.overview")?.data.kpis.alertsFiring ?? 0;
  if (firing <= 0) return null;
  return (
    <span className="num absolute -top-1 -right-1 flex min-w-4 items-center justify-center rounded-full bg-crit px-1 text-[10px] leading-4 font-semibold text-canvas">
      {firing}
    </span>
  );
}
