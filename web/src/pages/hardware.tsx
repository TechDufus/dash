import {
  Activity,
  CircleCheck,
  Cpu,
  Database,
  Fan as FanIcon,
  HardDrive,
  LoaderCircle,
  OctagonAlert,
  Power,
  TriangleAlert,
  Zap,
} from "lucide-react";
import { useReducedMotion } from "motion/react";
import type { ReactNode } from "react";
import { useTopic } from "@/api/stream";
import type { Disk, Health, HostData, Pool, SourceStatus } from "@/api/types";
import { BytesBar } from "@/components/bytes-bar";
import { Gauge } from "@/components/gauge";
import { LinkButtons } from "@/components/links";
import { NumberTicker } from "@/components/magicui/number-ticker";
import { Page, Reveal } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { SeriesChart } from "@/components/series-chart";
import { SparkSeries } from "@/components/spark";
import { HealthBadge, StatusDot } from "@/components/status";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import {
  ageSeconds,
  formatBytes,
  formatCelsius,
  formatDuration,
  formatNumber,
  formatPercent,
  formatRelative,
  formatWatts,
} from "@/lib/format";
import { HEALTH_TEXT, worstHealth } from "@/lib/health";
import { useNow } from "@/lib/hooks";
import { cn } from "@/lib/utils";

const fmtTemp = (v: number) => `${v.toFixed(0)}°C`;
const fmtRpm = (v: number) => `${Math.round(v)} rpm`;
const FAN_SCALE_MIN = 18000;
const BAY_COUNT = 16;
const SCRUB_STALE_DAYS = 35;

function sensorValue(host: HostData, needle: string): number | undefined {
  return host.sensors.find((s) => s.name.toLowerCase().includes(needle))?.valueC;
}

function cpuHealth(c: number | undefined): Health {
  if (c == null) return "unknown";
  if (c >= 90) return "crit";
  if (c >= 85) return "warn";
  return "ok";
}

function Label({ children }: { children: ReactNode }) {
  return <div className="text-xs font-medium tracking-wide text-muted uppercase">{children}</div>;
}

function BigPower({ watts }: { watts: number }) {
  const reduced = useReducedMotion();
  return (
    <span className="num text-6xl leading-none font-semibold text-fg">
      {reduced ? (
        formatNumber(watts)
      ) : (
        <NumberTicker value={Math.round(watts)} className="text-fg" />
      )}
    </span>
  );
}

function Hero({ host, sources }: { host: HostData; sources: SourceStatus[] | undefined }) {
  return (
    <Panel>
      <PanelHeader title={`${host.name} · Dell R730`} icon={<Zap />} sources={sources}>
        <LinkButtons links={host.links} />
      </PanelHeader>
      <div className="grid gap-4 px-4 pb-4 lg:grid-cols-[minmax(0,280px)_minmax(0,1fr)]">
        <div className="flex flex-col justify-center gap-2">
          <Label>Power draw</Label>
          <div className="flex items-baseline gap-2">
            <BigPower watts={host.powerWatts} />
            <span className="text-lg text-muted">W</span>
          </div>
          <span className="text-xs text-muted">Chassis input, 24 h trend alongside</span>
        </div>
        <SeriesChart
          name="host.power"
          range="24h"
          format={formatWatts}
          height={190}
          title="Power, 24 h"
        />
      </div>
    </Panel>
  );
}

function TempTile({
  label,
  value,
  health,
  detail,
  spark,
}: {
  label: string;
  value: number | undefined;
  health?: Health;
  detail?: ReactNode;
  spark?: ReactNode;
}) {
  return (
    <Panel className="flex min-h-[132px] flex-col justify-between gap-2 p-4">
      <div className="flex items-center gap-2">
        <Label>{label}</Label>
        {health && <StatusDot health={health} className="ml-auto" />}
      </div>
      <div className="flex items-baseline gap-2">
        <span
          className={cn("num text-3xl font-semibold", health ? HEALTH_TEXT[health] : "text-fg")}
        >
          {formatCelsius(value)}
        </span>
        {health && health !== "ok" && health !== "unknown" && (
          <span className={cn("text-xs font-medium", HEALTH_TEXT[health])}>
            {health === "crit" ? "Critical" : "Warning"}
          </span>
        )}
      </div>
      {detail && <div className="num text-xs text-muted">{detail}</div>}
      <div className="-mx-1 -mb-1 h-10">{spark}</div>
    </Panel>
  );
}

function Temps({ host }: { host: HostData }) {
  const inlet = sensorValue(host, "inlet");
  const exhaust = sensorValue(host, "exhaust");
  const pkgs = host.packageTempsC;
  const maxPkg = pkgs.length > 0 ? Math.max(...pkgs) : undefined;
  return (
    <div className="grid gap-4 sm:grid-cols-3">
      <TempTile label="Inlet" value={inlet} detail="Ambient air entering the chassis" />
      <TempTile label="Exhaust" value={exhaust} detail="Air leaving the chassis" />
      <TempTile
        label="CPU package"
        value={maxPkg}
        health={cpuHealth(maxPkg)}
        detail={
          pkgs.length > 0
            ? pkgs.map((t, i) => `CPU${i + 1} ${formatCelsius(t)}`).join(" · ")
            : "No package sensors"
        }
        spark={<SparkSeries name="host.cpuTemp" range="6h" format={fmtTemp} />}
      />
    </div>
  );
}

function Fact({ label, value, title }: { label: string; value: string; title?: string }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted">{label}</dt>
      <dd className="num truncate text-sm text-fg" title={title ?? value}>
        {value}
      </dd>
    </div>
  );
}

function System({ host }: { host: HostData }) {
  return (
    <Panel>
      <PanelHeader title="System" icon={<Cpu />} />
      <div className="space-y-4 px-4 pb-4">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-3">
          <Fact label="Uptime" value={formatDuration(host.uptimeSeconds)} />
          <Fact label="Proxmox VE" value={host.pveVersion || "–"} />
          <Fact label="Kernel" value={host.kernel || "–"} />
          <Fact label="CPU" value={host.cpuModel || "–"} />
        </dl>
        <div className="space-y-3">
          <div>
            <div className="mb-1 flex items-baseline justify-between text-xs">
              <span className="text-muted">CPU load</span>
              <span className="num text-fg">{formatPercent(host.cpuRatio)}</span>
            </div>
            <BytesBar used={host.cpuRatio} total={1} compact label="CPU load" />
          </div>
          <BytesBar used={host.memUsedBytes} total={host.memTotalBytes} label="Memory" />
        </div>
      </div>
    </Panel>
  );
}

function Fans({ host }: { host: HostData }) {
  const scale = Math.max(FAN_SCALE_MIN, ...host.fans.map((f) => f.rpm));
  return (
    <Panel>
      <PanelHeader title="Fans" icon={<FanIcon />} />
      {host.fans.length === 0 ? (
        <Empty>No fan readings.</Empty>
      ) : (
        <div className="grid grid-cols-2 gap-4 px-4 pb-4 sm:grid-cols-3">
          {host.fans.map((f) => (
            <div key={f.name} className="flex flex-col items-center gap-1">
              <Gauge
                value={f.rpm / scale}
                size={92}
                display={formatNumber(f.rpm)}
                caption="RPM"
                thresholds={false}
                label={`${f.name} ${Math.round(f.rpm)} RPM`}
              />
              <span className="text-xs text-muted">{f.name}</span>
              {f.rpm <= 0 && (
                <span className="inline-flex items-center gap-1 text-xs font-medium text-crit">
                  <OctagonAlert className="size-3" aria-hidden="true" />
                  Stopped
                </span>
              )}
            </div>
          ))}
        </div>
      )}
    </Panel>
  );
}

/** First run of digits in the bay label (the backend sends a plain number). */
function parseBay(bay: string): number | undefined {
  const m = /\d+/.exec(bay);
  return m ? Number.parseInt(m[0], 10) : undefined;
}

interface BaySlot {
  label: string;
  disk?: Disk;
}

/** Slots 0..15 by bay number; unparsable or colliding labels fall back to sorted order. */
function placeDisks(disks: Disk[]): BaySlot[] {
  const parsed = disks.map((d) => ({ d, n: parseBay(d.bay) }));
  const nums = parsed.map((p) => p.n);
  const allParsed = nums.every((n): n is number => n !== undefined);
  if (allParsed && new Set(nums).size === nums.length) {
    const min = Math.min(...nums);
    const max = Math.max(...nums);
    const offset = min >= 1 && max <= BAY_COUNT ? 1 : 0;
    if (max - offset < BAY_COUNT) {
      const slots: BaySlot[] = Array.from({ length: BAY_COUNT }, (_, i) => ({
        label: String(i + offset),
      }));
      for (const p of parsed) {
        const idx = (p.n ?? 0) - offset;
        const slot = slots[idx];
        if (slot) slot.disk = p.d;
      }
      return slots;
    }
  }
  const sorted = [...parsed].sort(
    (a, b) =>
      (a.n ?? Number.MAX_SAFE_INTEGER) - (b.n ?? Number.MAX_SAFE_INTEGER) ||
      a.d.bay.localeCompare(b.d.bay, undefined, { numeric: true }),
  );
  const total = Math.max(BAY_COUNT, sorted.length);
  return Array.from({ length: total }, (_, i) => {
    const p = sorted[i];
    return p ? { label: p.d.bay, disk: p.d } : { label: String(i + 1) };
  });
}

function diskHealth(d: Disk): Health {
  if (!d.healthy) return "crit";
  const life = d.lifeRemainingPct;
  if (life != null) {
    if (life < 10) return "crit";
    if (life < 20) return "warn";
  }
  return "ok";
}

const BAY_STYLE: Record<Health, string> = {
  ok: "border-ok/25 bg-ok/5",
  warn: "border-warn/40 bg-warn/10",
  crit: "border-crit/40 bg-crit/10",
  unknown: "border-line bg-panel",
};

function BayIcon({ health }: { health: Health }) {
  const cls = cn("size-3.5", HEALTH_TEXT[health]);
  if (health === "crit") return <OctagonAlert className={cls} aria-hidden="true" />;
  if (health === "warn") return <TriangleAlert className={cls} aria-hidden="true" />;
  return <CircleCheck className={cls} aria-hidden="true" />;
}

function Bay({ slot }: { slot: BaySlot }) {
  const d = slot.disk;
  if (!d) {
    return (
      <div className="flex min-h-[84px] flex-col justify-between rounded-lg border border-dashed border-line p-2 opacity-50">
        <span className="num text-[10px] text-faint">#{slot.label}</span>
        <span className="text-center text-[11px] text-faint">empty</span>
        <span />
      </div>
    );
  }
  const health = diskHealth(d);
  const label = `Bay ${slot.label}: ${d.healthy ? "healthy" : "SMART failing"}`;
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label={label}
          className={cn(
            "flex min-h-[84px] w-full flex-col justify-between rounded-lg border p-2 text-left outline-none",
            BAY_STYLE[health],
          )}
        >
          <span className="flex items-center justify-between">
            <span className="num text-[10px] text-muted">#{slot.label}</span>
            <BayIcon health={health} />
          </span>
          <span className="num text-sm font-medium text-fg">{formatCelsius(d.tempC)}</span>
          <span className={cn("num text-[11px]", d.healthy ? HEALTH_TEXT[health] : "text-crit")}>
            {!d.healthy
              ? "SMART fail"
              : d.lifeRemainingPct != null
                ? `${Math.round(d.lifeRemainingPct)}% life`
                : "life –"}
          </span>
        </button>
      </TooltipTrigger>
      <TooltipContent className="flex-col items-start gap-0.5">
        <span className="font-medium">Bay {slot.label}</span>
        <span className="num">Device {d.device || "–"}</span>
        <span className="num">Serial {d.serial || "–"}</span>
      </TooltipContent>
    </Tooltip>
  );
}

function Bays({ host }: { host: HostData }) {
  const slots = placeDisks(host.disks);
  const failing = host.disks.filter((d) => !d.healthy).length;
  return (
    <Panel className="lg:col-span-2">
      <PanelHeader title="Disk bays" icon={<HardDrive />}>
        <span className="num text-xs text-muted">
          {host.disks.length} populated
          {failing > 0 && <span className="ml-2 font-medium text-crit">{failing} failing</span>}
        </span>
      </PanelHeader>
      <div className="grid grid-cols-4 gap-2 px-4 pb-4 sm:grid-cols-8">
        {slots.map((s, i) => (
          // Slot position is the identity of a bay; the label alone can repeat in the fallback order.
          // biome-ignore lint/suspicious/noArrayIndexKey: fixed grid positions
          <Bay key={i} slot={s} />
        ))}
      </div>
    </Panel>
  );
}

function poolHealth(state: string): Health {
  const s = state.toUpperCase();
  if (s === "ONLINE") return "ok";
  if (s === "DEGRADED") return "warn";
  return "crit";
}

function PoolCard({ pool, now }: { pool: Pool; now: number }) {
  const age = ageSeconds(pool.lastScrubEnd, now);
  const stale = age == null || age > SCRUB_STALE_DAYS * 86400;
  return (
    <div className="space-y-2 rounded-xl border border-line bg-panel p-3">
      <div className="flex items-center gap-2">
        <Database className="size-4 text-accent" aria-hidden="true" />
        <span className="num text-sm font-medium text-fg">{pool.name}</span>
        <HealthBadge
          className="ml-auto"
          health={poolHealth(pool.state)}
          label={pool.state.toUpperCase()}
        />
      </div>
      <dl className="grid grid-cols-2 gap-2 text-xs">
        <div>
          <dt className="text-muted">Last scrub</dt>
          <dd className={cn("num inline-flex items-center gap-1", stale ? "text-warn" : "text-fg")}>
            {stale && <TriangleAlert className="size-3" aria-hidden="true" />}
            {pool.lastScrubEnd ? formatRelative(pool.lastScrubEnd, now) : "never"}
            {stale && pool.lastScrubEnd && " · stale"}
          </dd>
        </div>
        <div>
          <dt className="text-muted">Scrub errors</dt>
          <dd className={cn("num", pool.scrubErrors > 0 ? "font-medium text-crit" : "text-fg")}>
            {pool.scrubErrors}
          </dd>
        </div>
      </dl>
      {pool.scrubInProgress && (
        <span className="inline-flex items-center gap-1 rounded-full border border-accent/30 bg-accent/10 px-2 py-0.5 text-xs text-accent">
          <LoaderCircle className="size-3 animate-spin" aria-hidden="true" />
          Scrub in progress
        </span>
      )}
    </div>
  );
}

function SidePanels({ host, now }: { host: HostData; now: number }) {
  const psuHealth = worstHealth(host.psus.map((p) => p.state));
  return (
    <div className="space-y-4">
      <Panel>
        <PanelHeader title="ZFS pools" icon={<Database />} />
        <div className="space-y-2 px-4 pb-4">
          {host.pools.length === 0 ? (
            <Empty className="py-4">No pools reported.</Empty>
          ) : (
            host.pools.map((p) => <PoolCard key={p.name} pool={p} now={now} />)
          )}
        </div>
      </Panel>
      <Panel>
        <PanelHeader title="Power supplies" icon={<Power />}>
          {host.psus.length > 0 && <StatusDot health={psuHealth} />}
        </PanelHeader>
        <div className="space-y-2 px-4 pb-4">
          {host.psus.length === 0 ? (
            <Empty className="py-4">No PSU readings.</Empty>
          ) : (
            host.psus.map((p) => (
              <div key={p.name} className="flex items-center justify-between gap-2 text-sm">
                <span className="text-fg">{p.name}</span>
                <HealthBadge health={p.state} />
              </div>
            ))
          )}
        </div>
      </Panel>
      <Panel>
        <PanelHeader title="iDRAC log" icon={<Activity />} />
        <div className="space-y-1 px-4 pb-4">
          <div className="num text-2xl font-semibold text-fg">{formatBytes(host.selFreeBytes)}</div>
          <p className="text-xs text-muted">free in the iDRAC System Event Log</p>
        </div>
      </Panel>
    </div>
  );
}

function Trends() {
  return (
    <Panel>
      <Tabs defaultValue="temps" className="gap-0">
        <div className="flex items-center gap-3 px-4 pt-4 pb-3">
          <Activity className="size-4 text-accent" aria-hidden="true" />
          <h2 className="text-sm font-medium tracking-wide text-fg">Trends, 24 h</h2>
          <TabsList variant="line" className="ml-auto">
            <TabsTrigger value="temps">Temperatures</TabsTrigger>
            <TabsTrigger value="fans">Fans</TabsTrigger>
          </TabsList>
        </div>
        <TabsContent value="temps" className="px-4 pb-4">
          <SeriesChart name="host.temps" range="24h" format={fmtTemp} height={220} />
        </TabsContent>
        <TabsContent value="fans" className="px-4 pb-4">
          <SeriesChart name="host.fans" range="24h" format={fmtRpm} height={220} />
        </TabsContent>
      </Tabs>
    </Panel>
  );
}

export function HardwarePage() {
  const env = useTopic("host");
  const host = env?.data;
  const now = useNow(30_000).getTime();

  if (!host) {
    return (
      <Page>
        <PanelSkeleton lines={4} className="min-h-[260px]" />
        <div className="grid gap-4 sm:grid-cols-3">
          <PanelSkeleton lines={2} />
          <PanelSkeleton lines={2} />
          <PanelSkeleton lines={2} />
        </div>
        <div className="grid gap-4 lg:grid-cols-2">
          <PanelSkeleton lines={4} />
          <PanelSkeleton lines={4} />
        </div>
        <div className="grid gap-4 lg:grid-cols-3">
          <PanelSkeleton lines={6} className="lg:col-span-2" />
          <PanelSkeleton lines={6} />
        </div>
      </Page>
    );
  }

  return (
    <Page>
      <Reveal index={0}>
        <Hero host={host} sources={env?.sources} />
      </Reveal>
      <Reveal index={1}>
        <Temps host={host} />
      </Reveal>
      <Reveal index={2} className="grid gap-4 lg:grid-cols-2">
        <System host={host} />
        <Fans host={host} />
      </Reveal>
      <Reveal index={3} className="grid gap-4 lg:grid-cols-3 lg:items-start">
        <Bays host={host} />
        <SidePanels host={host} now={now} />
      </Reveal>
      <Reveal index={4}>
        <Trends />
      </Reveal>
    </Page>
  );
}
