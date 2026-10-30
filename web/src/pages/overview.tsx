import {
  BellRing,
  Boxes,
  Flame,
  HardDrive,
  Monitor,
  ShieldAlert,
  ShieldCheck,
  Zap,
} from "lucide-react";
import { Link } from "wouter";
import { useTopic } from "@/api/stream";
import type { Health, Kpis, OverviewData } from "@/api/types";
import { Kpi } from "@/components/kpi";
import { Launcher } from "@/components/launcher";
import { Page, Reveal } from "@/components/page";
import { PanelSkeleton } from "@/components/panel";
import { SparkSeries } from "@/components/spark";
import { Skeleton } from "@/components/ui/skeleton";
import { INFRA_LINKS } from "@/config/links";
import { formatCelsius, formatPercent, formatWatts } from "@/lib/format";
import { HEALTH_COLOR, HEALTH_TEXT, ratioHealth } from "@/lib/health";
import { cn } from "@/lib/utils";
import { AttentionPanel } from "./attention";
import { TopologyPanel, tempHealth } from "./topology";

function alertsHealth(data: OverviewData): Health {
  if (data.attention.some((a) => a.severity === "crit")) return "crit";
  if (data.kpis.alertsFiring > 0 || data.attention.some((a) => a.severity === "warn"))
    return "warn";
  return "ok";
}

function KpiLink({
  href,
  label,
  children,
}: {
  href: string;
  label: string;
  children: React.ReactNode;
}) {
  return (
    <Link href={href} aria-label={label} className="block rounded-2xl">
      {children}
    </Link>
  );
}

function KpiRow({ data }: { data: OverviewData }) {
  const k: Kpis = data.kpis;
  const temp = tempHealth(k.cpuTempMaxC);
  const gitops: Health = k.fluxReady < k.fluxTotal || !k.sourceVerified ? "warn" : "ok";
  const nodes: Health = k.nodesReady < k.nodesTotal ? "crit" : "ok";
  const vms: Health = k.vmsRunning < k.vmsTotal ? "warn" : "ok";
  const gitopsNote = [
    k.sourceVerified ? "Source verified" : "Source not verified",
    k.fluxSuspended > 0 ? `${k.fluxSuspended} suspended` : "",
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
      <KpiLink href="/alerts" label={`Alerts firing: ${k.alertsFiring}. Open alerts`}>
        <Kpi
          label="Alerts firing"
          value={k.alertsFiring}
          health={alertsHealth(data)}
          icon={<BellRing aria-hidden="true" />}
          className="h-full transition-colors hover:border-accent/30"
        />
      </KpiLink>
      <KpiLink href="/gitops" label={`GitOps ${k.fluxReady} of ${k.fluxTotal} ready. Open GitOps`}>
        <Kpi
          label="GitOps"
          value={k.fluxReady}
          suffix={`/ ${k.fluxTotal}`}
          health={gitops}
          icon={
            k.sourceVerified ? (
              <ShieldCheck className="text-ok" role="img" aria-label="Source verified" />
            ) : (
              <ShieldAlert className="text-warn" role="img" aria-label="Source not verified" />
            )
          }
          spark={
            <p
              className={cn(
                "flex h-full items-end px-1 text-xs",
                k.sourceVerified && k.fluxSuspended === 0 ? "text-muted" : "text-warn",
              )}
            >
              {gitopsNote}
            </p>
          }
          className="h-full transition-colors hover:border-accent/30"
        />
      </KpiLink>
      <KpiLink
        href="/cluster"
        label={`Nodes ${k.nodesReady} of ${k.nodesTotal} ready. Open cluster`}
      >
        <Kpi
          label="Nodes"
          value={k.nodesReady}
          suffix={`/ ${k.nodesTotal}`}
          health={nodes}
          icon={<Boxes aria-hidden="true" />}
          className="h-full transition-colors hover:border-accent/30"
        />
      </KpiLink>
      <KpiLink href="/vms" label={`VMs ${k.vmsRunning} of ${k.vmsTotal} running. Open VMs`}>
        <Kpi
          label="VMs"
          value={k.vmsRunning}
          suffix={`/ ${k.vmsTotal}`}
          health={vms}
          icon={<Monitor aria-hidden="true" />}
          className="h-full transition-colors hover:border-accent/30"
        />
      </KpiLink>
      <KpiLink href="/hardware" label={`Power ${formatWatts(k.powerWatts)}. Open hardware`}>
        <Kpi
          label="Power"
          value={k.powerWatts}
          unit="W"
          icon={<Zap aria-hidden="true" />}
          spark={<SparkSeries name="host.power" range="6h" format={formatWatts} />}
          className="h-full transition-colors hover:border-accent/30"
        />
      </KpiLink>
      <KpiLink
        href="/hardware"
        label={`CPU temperature ${formatCelsius(k.cpuTempMaxC)}. Open hardware`}
      >
        <Kpi
          label="CPU temp"
          value={k.cpuTempMaxC}
          unit="°C"
          health={temp}
          icon={<Flame aria-hidden="true" />}
          spark={
            <SparkSeries
              name="host.cpuTemp"
              range="6h"
              color={temp === "ok" ? undefined : HEALTH_COLOR[temp]}
              format={formatCelsius}
            />
          }
          className="h-full transition-colors hover:border-accent/30"
        />
      </KpiLink>
    </div>
  );
}

function WorstPvc({ pvc }: { pvc: NonNullable<Kpis["worstPvc"]> }) {
  const health = ratioHealth(pvc.usedRatio);
  return (
    <Link
      href="/storage"
      className="inline-flex max-w-full items-center gap-2 rounded-lg px-1 py-0.5 text-xs text-muted hover:text-fg"
    >
      <HardDrive className="size-3.5 shrink-0" aria-hidden="true" />
      <span className="truncate">
        Fullest volume: {pvc.namespace}/{pvc.name}
      </span>
      <span className={cn("num shrink-0 font-medium", HEALTH_TEXT[health])}>
        {formatPercent(pvc.usedRatio)}
      </span>
    </Link>
  );
}

function OverviewSkeleton() {
  return (
    <>
      <div className="grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6" aria-busy="true">
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
  const overview = useTopic("overview");
  const apps = useTopic("apps");
  const data = overview?.data;
  return (
    <Page>
      {data ? (
        <>
          <Reveal index={0} className="space-y-2">
            <KpiRow data={data} />
            {data.kpis.worstPvc && <WorstPvc pvc={data.kpis.worstPvc} />}
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
      <Reveal index={2}>
        <Launcher
          title="Launcher"
          apps={apps?.data.apps}
          infra={INFRA_LINKS}
          sources={apps?.sources}
          compact
        />
      </Reveal>
    </Page>
  );
}
