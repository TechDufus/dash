import { HardDrive, LayoutGrid } from "lucide-react";
import { Link } from "wouter";
import { useTopic } from "@/api/stream";
import type { OverviewData } from "@/api/types";
import { Kpi } from "@/components/kpi";
import { useLauncherShown } from "@/components/launcher";
import { Page, Reveal } from "@/components/page";
import { PanelSkeleton, SourceChips } from "@/components/panel";
import { Skeleton } from "@/components/ui/skeleton";
import { formatPercent } from "@/lib/format";
import { HEALTH_TEXT } from "@/lib/health";
import { cn } from "@/lib/utils";
import { contributesPanel, Slot } from "@/plugins/slots";
import { isVisible, usePluginsState } from "@/plugins/state";
import { AttentionPanel } from "./attention";
import { TopologyPanel } from "./topology";

const KPI_GRID = "grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6";

/**
 * The KPIs plugins publish in `kpiList`, after the tiles the modules draw; hidden with their plugin.
 * A plugin that contributes an `overview.kpis` panel owns the presentation of all its KPIs, so its
 * list entries are not drawn a second time here.
 */
function PublishedKpis({ data }: { data: OverviewData }) {
  const { byId } = usePluginsState();
  return data.kpiList
    .filter((k) => {
      const info = byId.get(k.plugin);
      return (
        info !== undefined && isVisible(info.state) && !contributesPanel(k.plugin, "overview.kpis")
      );
    })
    .map((k) => (
      <Kpi
        key={`${k.plugin}/${k.key}`}
        label={k.label}
        value={k.value}
        unit={k.unit}
        health={k.health}
        className="h-full"
      />
    ));
}

/**
 * The fullest volume, from the kubernetes plugin's `pvc.worst` KPI (value is the used ratio, label
 * is `<namespace>/<name>`). The plugin's tile owns its other KPIs; this line is the page's own, and
 * goes with the plugin.
 */
function WorstPvc({ data }: { data: OverviewData }) {
  const { byId } = usePluginsState();
  const info = byId.get("kubernetes");
  const pvc = data.kpiList.find((k) => k.plugin === "kubernetes" && k.key === "pvc.worst");
  if (!pvc || !info || !isVisible(info.state)) return null;
  return (
    <Link
      href="/storage"
      className="inline-flex max-w-full items-center gap-2 rounded-lg px-1 py-0.5 text-xs text-muted hover:text-fg"
    >
      <HardDrive className="size-3.5 shrink-0" aria-hidden="true" />
      <span className="truncate">Fullest volume: {pvc.label}</span>
      <span className={cn("num shrink-0 font-medium", HEALTH_TEXT[pvc.health])}>
        {formatPercent(pvc.value)}
      </span>
    </Link>
  );
}

function OverviewSkeleton() {
  return (
    <>
      <div className={KPI_GRID} aria-busy="true">
        {Array.from({ length: 6 }, (_, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: static placeholder tiles
          <Skeleton key={i} className="h-[118px] rounded-2xl" />
        ))}
      </div>
      <div className="grid gap-4 lg:grid-cols-12">
        <PanelSkeleton className="min-h-[420px] lg:col-span-8" lines={5} />
        <PanelSkeleton className="min-h-[420px] lg:col-span-4" lines={4} />
      </div>
    </>
  );
}

export function OverviewPage() {
  const overview = useTopic("core.overview");
  const data = overview?.data;
  const launcherShown = useLauncherShown();
  const appSources = useTopic("kubernetes.apps")?.sources;
  return (
    <Page>
      {data ? (
        <>
          <Reveal index={0} className="space-y-2">
            <div className={KPI_GRID}>
              <Slot id="overview.kpis" data={data} />
              <PublishedKpis data={data} />
            </div>
            <WorstPvc data={data} />
          </Reveal>
          <Reveal index={1} className="grid gap-4 lg:grid-cols-12">
            <TopologyPanel
              topology={data.topology}
              sources={overview.sources}
              className="lg:col-span-8"
            />
            <AttentionPanel
              items={data.attention}
              sources={overview.sources}
              className="lg:col-span-4"
            />
          </Reveal>
        </>
      ) : (
        <OverviewSkeleton />
      )}
      {launcherShown && (
        <Reveal index={2} className="space-y-5">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <h2 className="flex items-center gap-2 text-sm font-medium tracking-wide text-fg">
              <LayoutGrid className="size-4 text-accent" aria-hidden="true" />
              Launcher
            </h2>
            <SourceChips sources={appSources} />
          </div>
          <Slot id="apps.sources" query="" compact />
        </Reveal>
      )}
    </Page>
  );
}
