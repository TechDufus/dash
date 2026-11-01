import { useTopic } from "@/api/stream";

/** The firing-alert count, drawn on the Alerts rail entry: the plugin's `firing` KPI. */
export function AlertsBadge() {
  const kpis = useTopic("core.overview")?.data.kpiList;
  const firing = kpis?.find((k) => k.plugin === "alertmanager" && k.key === "firing")?.value ?? 0;
  if (firing <= 0) return null;
  return (
    <span className="num absolute -top-1 -right-1 flex min-w-4 items-center justify-center rounded-full bg-crit px-1 text-[10px] leading-4 font-semibold text-canvas">
      {firing}
    </span>
  );
}
