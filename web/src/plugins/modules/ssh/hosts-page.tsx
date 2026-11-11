import {
  Activity,
  Box,
  Database,
  Fan as FanIcon,
  HardDrive,
  Info as InfoIcon,
  ListChecks,
  OctagonAlert,
  Server,
  Thermometer,
} from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "wouter";
import { settingsAvailable, useMe } from "@/api/queries";
import { useTopic } from "@/api/stream";
import type { Health } from "@/api/types";
import { BytesBar } from "@/components/bytes-bar";
import { Gauge } from "@/components/gauge";
import { Page, Reveal } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { Spark } from "@/components/spark";
import { HealthBadge, StatusDot } from "@/components/status";
import {
  formatBytes,
  formatCelsius,
  formatDuration,
  formatNumber,
  formatPercent,
  formatRate,
  formatRelative,
  shortImage,
} from "@/lib/format";
import { HEALTH_TEXT } from "@/lib/health";
import { useNow } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { FleetGrid, SetupLink } from "./fleet";
import { useFeedHistories, useHostHistory } from "./history";
import { findHost, SETTINGS_PATH, stateLabel } from "./hosts";
import { type ChartLine, LiveChart } from "./live-chart";
import type { Containers, Host, Info, Pool, Sample, Services, Stats } from "./model.gen";

const RX = "var(--color-accent)";
const TX = "#a78bfa";

const CPU_LINES: ChartLine[] = [{ field: "cpuRatio", label: "CPU", color: RX }];
const MEM_LINES: ChartLine[] = [{ field: "memRatio", label: "Memory", color: "var(--color-ok)" }];
const NET_LINES: ChartLine[] = [
  { field: "netRxBytesPerSec", label: "Receive", color: RX },
  { field: "netTxBytesPerSec", label: "Transmit", color: TX },
];
const DISK_LINES: ChartLine[] = [
  { field: "diskReadBytesPerSec", label: "Read", color: RX },
  { field: "diskWriteBytesPerSec", label: "Write", color: TX },
];

function poolHealth(state: string): Health {
  const s = state.toUpperCase();
  if (s === "ONLINE") return "ok";
  if (s === "DEGRADED") return "warn";
  return "crit";
}

/** One fact of the header: left out by the caller when the host does not report it. */
function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs text-muted">{label}</dt>
      <dd className="num truncate text-sm text-fg">{children}</dd>
    </div>
  );
}

/** The facts of the header the host reports, in order; an empty string or zero is not reported. */
export function hostFacts(info: Info | undefined, stats: Stats | undefined): [string, string][] {
  const out: [string, string][] = [];
  const add = (label: string, value: string) => {
    if (value.trim() !== "") out.push([label, value]);
  };
  if (info) {
    add("OS", info.os);
    add("Kernel", info.kernel);
    add("Architecture", info.arch);
    add("System", [info.vendor, info.product].filter((p) => p.trim() !== "").join(" "));
    add("Virtualization", info.virtualization);
    const cpus =
      info.cpus > 0 ? `${formatNumber(info.cpus)} ${info.cpus === 1 ? "CPU" : "CPUs"}` : "";
    add("Processor", [info.cpuModel, cpus].filter((p) => p !== "").join(" · "));
  }
  if (stats) {
    if (stats.memTotalBytes > 0) add("Memory", formatBytes(stats.memTotalBytes));
    if (stats.uptimeSeconds > 0) add("Uptime", formatDuration(stats.uptimeSeconds));
    add(
      "Load",
      [stats.load1, stats.load5, stats.load15].map((l) => formatNumber(l, 2)).join(" · "),
    );
  }
  return out;
}

function Header({ host, admin, now }: { host: Host; admin: boolean; now: number }) {
  const facts = hostFacts(host.info, host.stats);
  const up = host.state === "up";
  return (
    <Panel>
      <PanelHeader title={host.name} icon={<Server />}>
        <HealthBadge health={host.health} label={stateLabel(host.state)} />
      </PanelHeader>
      <div className="space-y-4 px-4 pb-4">
        {!up && (
          <div
            role="status"
            className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-line bg-panel-hover px-3 py-2 text-sm"
          >
            <span className="text-fg">{host.message}</span>
            <span className="num text-xs text-muted">since {formatRelative(host.since, now)}</span>
            {admin && <SetupLink host={host} className="ml-auto" />}
          </div>
        )}
        {facts.length > 0 && (
          <dl className="grid grid-cols-2 gap-x-6 gap-y-3 sm:grid-cols-3 xl:grid-cols-4">
            {facts.map(([label, value]) => (
              <Fact key={label} label={label}>
                {value}
              </Fact>
            ))}
          </dl>
        )}
        {up && host.stats && (
          <div className="flex flex-wrap items-center gap-6">
            <Gauge
              value={host.stats.cpuRatio}
              size={84}
              caption="CPU"
              label={`CPU ${formatPercent(host.stats.cpuRatio)}`}
            />
            <div className="min-w-[200px] flex-1 space-y-3">
              {host.stats.memTotalBytes > 0 && (
                <BytesBar
                  used={host.stats.memUsedBytes}
                  total={host.stats.memTotalBytes}
                  label="Memory"
                />
              )}
              {host.stats.swapTotalBytes > 0 && (
                <BytesBar
                  used={host.stats.swapUsedBytes}
                  total={host.stats.swapTotalBytes}
                  label="Swap"
                />
              )}
              <div>
                <div className="mb-1 flex items-baseline justify-between text-xs">
                  <span className="text-muted">Waiting for I/O</span>
                  <span className="num text-fg">{formatPercent(host.stats.ioWaitRatio)}</span>
                </div>
                <BytesBar
                  used={host.stats.ioWaitRatio}
                  total={1}
                  compact
                  neutral
                  label="Waiting for I/O"
                />
              </div>
            </div>
          </div>
        )}
      </div>
    </Panel>
  );
}

interface HistoryView {
  samples: readonly Sample[];
  interval: number;
  pending: boolean;
  error: boolean;
}

function Charts({ host, history }: { host: Host; history: HistoryView }) {
  const { samples, interval, pending } = history;
  // Memory a host does not report is left out rather than drawn as an empty chart.
  const memory = !host.stats || host.stats.memTotalBytes > 0;
  return (
    <Panel>
      <PanelHeader title="Last hour" icon={<Activity />}>
        {history.error && <span className="text-xs text-muted">history unavailable</span>}
      </PanelHeader>
      <div className="grid gap-x-6 gap-y-5 px-4 pb-4 lg:grid-cols-2">
        <LiveChart
          title="CPU"
          samples={samples}
          lines={CPU_LINES}
          intervalSeconds={interval}
          format={formatPercent}
          domain={[0, 1]}
          pending={pending}
        />
        {memory && (
          <LiveChart
            title="Memory"
            samples={samples}
            lines={MEM_LINES}
            intervalSeconds={interval}
            format={formatPercent}
            domain={[0, 1]}
            pending={pending}
          />
        )}
        <LiveChart
          title="Network"
          samples={samples}
          lines={NET_LINES}
          intervalSeconds={interval}
          format={formatRate}
          pending={pending}
        />
        <LiveChart
          title="Disk I/O"
          samples={samples}
          lines={DISK_LINES}
          intervalSeconds={interval}
          format={formatRate}
          pending={pending}
        />
      </div>
    </Panel>
  );
}

function Sensors({ stats, samples }: { stats: Stats; samples: readonly Sample[] }) {
  const temps = samples.flatMap((s) => (s.cpuTempC != null ? [[s.t, s.cpuTempC]] : []));
  // The fan gauges scale to the fastest fan seen: no host reports a maximum.
  const scale = Math.max(0, ...stats.fans.map((f) => f.rpm));
  return (
    <div className="grid gap-4 lg:grid-cols-2">
      {stats.sensors.length > 0 && (
        <Panel>
          <PanelHeader title="Temperatures" icon={<Thermometer />}>
            {stats.cpuTempC != null && (
              <span className="num text-xs text-muted">CPU {formatCelsius(stats.cpuTempC)}</span>
            )}
          </PanelHeader>
          <div className="space-y-3 px-4 pb-4">
            {temps.length > 1 && (
              <div className="-mx-1" title="Hottest CPU reading, last hour">
                <Spark points={temps} format={formatCelsius} />
              </div>
            )}
            <ul className="grid grid-cols-1 gap-x-6 gap-y-1.5 sm:grid-cols-2">
              {stats.sensors.map((s) => (
                <li
                  key={`${s.chip}/${s.label}`}
                  className="flex min-w-0 items-baseline justify-between gap-2 text-sm"
                >
                  <span className="truncate text-muted">
                    {s.label} <span className="text-xs text-faint">{s.chip}</span>
                  </span>
                  <span className="num shrink-0 text-fg">{formatCelsius(s.tempC)}</span>
                </li>
              ))}
            </ul>
          </div>
        </Panel>
      )}
      {stats.fans.length > 0 && (
        <Panel>
          <PanelHeader title="Fans" icon={<FanIcon />} />
          <ul className="grid grid-cols-2 gap-4 px-4 pb-4 sm:grid-cols-3">
            {stats.fans.map((f) => (
              <li key={`${f.chip}/${f.label}`} className="flex flex-col items-center gap-1">
                <Gauge
                  value={scale > 0 ? f.rpm / scale : 0}
                  size={84}
                  display={formatNumber(f.rpm)}
                  caption="RPM"
                  thresholds={false}
                  label={`${f.label} ${formatNumber(f.rpm)} RPM`}
                />
                <span className="max-w-full truncate text-xs text-muted">{f.label}</span>
                {f.rpm <= 0 && (
                  <span className="inline-flex items-center gap-1 text-xs font-medium text-crit">
                    <OctagonAlert className="size-3" aria-hidden="true" />
                    Stopped
                  </span>
                )}
              </li>
            ))}
          </ul>
        </Panel>
      )}
    </div>
  );
}

function Filesystems({ info }: { info: Info }) {
  if (info.filesystems.length === 0) return null;
  return (
    <Panel>
      <PanelHeader title="File systems" icon={<HardDrive />} />
      <ul className="space-y-3 px-4 pb-4">
        {info.filesystems.map((f) => (
          <li key={f.mount} className="space-y-1">
            <BytesBar used={f.usedBytes} total={f.totalBytes} label={f.mount} />
            {(f.device || f.type) && (
              <div className="num truncate text-[11px] text-faint">
                {[f.device, f.type].filter((p) => p !== "").join(" · ")}
              </div>
            )}
          </li>
        ))}
      </ul>
    </Panel>
  );
}

const KIND: Record<string, string> = { hdd: "HDD", ssd: "SSD", nvme: "NVMe" };

function Disks({ info }: { info: Info }) {
  if (info.disks.length === 0) return null;
  return (
    <Panel>
      <PanelHeader title="Disks" icon={<HardDrive />}>
        <span className="num text-xs text-muted">
          {formatBytes(info.disks.reduce((n, d) => n + d.sizeBytes, 0))} in {info.disks.length}
        </span>
      </PanelHeader>
      <ul className="divide-y divide-line px-4 pb-2">
        {info.disks.map((d) => (
          <li key={d.name} className="flex min-w-0 items-center gap-3 py-2 text-sm">
            <span className="num shrink-0 text-fg">{d.name}</span>
            {d.model && <span className="min-w-0 truncate text-muted">{d.model}</span>}
            <span className="ml-auto flex shrink-0 items-center gap-2">
              {KIND[d.kind] && (
                <span className="rounded-full border border-line px-1.5 py-0.5 text-[11px] text-muted">
                  {KIND[d.kind]}
                </span>
              )}
              {d.sizeBytes > 0 && <span className="num text-fg">{formatBytes(d.sizeBytes)}</span>}
            </span>
          </li>
        ))}
      </ul>
    </Panel>
  );
}

function PoolRow({ pool }: { pool: Pool }) {
  return (
    <li className="space-y-2 rounded-xl border border-line bg-panel p-3">
      <div className="flex items-center gap-2">
        <Database className="size-4 text-accent" aria-hidden="true" />
        <span className="num text-sm font-medium text-fg">{pool.name}</span>
        <HealthBadge
          className="ml-auto"
          health={poolHealth(pool.health)}
          label={pool.health.toUpperCase()}
        />
      </div>
      {pool.sizeBytes > 0 && (
        <BytesBar used={pool.allocBytes} total={pool.sizeBytes} label="Allocated" />
      )}
    </li>
  );
}

function Pools({ info }: { info: Info }) {
  if (info.pools.length === 0) return null;
  return (
    <Panel>
      <PanelHeader title="ZFS pools" icon={<Database />} />
      <ul className="space-y-2 px-4 pb-4">
        {info.pools.map((p) => (
          <PoolRow key={p.name} pool={p} />
        ))}
      </ul>
    </Panel>
  );
}

const RUNTIME: Record<string, string> = { docker: "Docker", podman: "Podman" };

function ContainersPanel({ containers }: { containers: Containers }) {
  const runtime = RUNTIME[containers.runtime] ?? containers.runtime;
  const running = containers.items.filter((c) => c.state === "running").length;
  return (
    <Panel>
      <PanelHeader title="Containers" icon={<Box />}>
        <span className="text-xs text-muted">{runtime}</span>
        {!containers.denied && (
          <span className="num text-xs text-muted">
            {running} of {containers.items.length} running
          </span>
        )}
      </PanelHeader>
      {containers.denied ? (
        <div className="space-y-1 px-4 pb-4 text-sm">
          <p className="text-fg">
            {runtime} runs here, and the SSH user may not ask it which containers it runs, so none
            are listed.
          </p>
          <p className="text-xs text-muted">
            {containers.runtime === "docker"
              ? "Access to Docker is access to the whole host (whoever may use the docker socket can start a privileged container), so dash does not ask for it. Add the user to the docker group only if you accept that."
              : "Podman lists the containers of the user it runs as: run them as the SSH user, or leave them unlisted."}
          </p>
        </div>
      ) : containers.items.length === 0 ? (
        <Empty className="py-4">No containers.</Empty>
      ) : (
        <ul className="divide-y divide-line px-4 pb-2">
          {containers.items.map((c) => {
            const on = c.state === "running";
            return (
              <li key={c.name} className="flex min-w-0 items-center gap-3 py-2 text-sm">
                <StatusDot health={on ? "ok" : "unknown"} />
                <div className="min-w-0 flex-1">
                  <div className="truncate text-fg">{c.name}</div>
                  <div className="num truncate text-[11px] text-faint" title={c.image}>
                    {shortImage(c.image)}
                  </div>
                </div>
                <span className={cn("shrink-0 text-xs", on ? "text-ok" : "text-muted")}>
                  {c.status || c.state}
                </span>
              </li>
            );
          })}
        </ul>
      )}
    </Panel>
  );
}

function ServicesPanel({ services }: { services: Services }) {
  const failed = services.failed.length;
  return (
    <Panel>
      <PanelHeader title="Systemd services" icon={<ListChecks />}>
        <StatusDot health={failed > 0 ? "warn" : "ok"} />
      </PanelHeader>
      <div className="space-y-3 px-4 pb-4">
        <dl className="grid grid-cols-2 gap-4">
          <div>
            <dt className="text-xs text-muted">Running</dt>
            <dd className="num text-2xl font-semibold text-fg">{formatNumber(services.running)}</dd>
          </div>
          <div>
            <dt className="text-xs text-muted">Failed</dt>
            <dd
              className={cn(
                "num text-2xl font-semibold",
                failed > 0 ? HEALTH_TEXT.warn : "text-fg",
              )}
            >
              {formatNumber(failed)}
            </dd>
          </div>
        </dl>
        {failed > 0 && (
          <ul aria-label="Failed units" className="space-y-1">
            {services.failed.map((u) => (
              <li key={u} className="num flex items-center gap-2 text-sm text-warn">
                <OctagonAlert className="size-3.5 shrink-0" aria-hidden="true" />
                <span className="truncate">{u}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </Panel>
  );
}

/** One host: its header, the charts, the sensors, its storage and what runs on it. */
function HostDetail({ host, admin, now }: { host: Host; admin: boolean; now: number }) {
  const q = useHostHistory(host.name, true);
  const history: HistoryView = {
    samples: q.data?.samples ?? [],
    // 0 is a history without samples; the plugin's default interval stands in.
    interval: q.data?.intervalSeconds || 5,
    pending: q.isPending,
    error: q.isError,
  };
  const info = host.info;
  const stats = host.state === "up" ? host.stats : undefined;
  // A host that is away and has no history has nothing to chart.
  const charts = host.state === "up" || history.pending || history.samples.length > 0;
  const sensors = stats && (stats.sensors.length > 0 || stats.fans.length > 0);
  const storage =
    info && (info.filesystems.length > 0 || info.disks.length > 0 || info.pools.length > 0);
  const workloads = info && (info.containers || info.services);
  return (
    <>
      <Reveal index={1}>
        <Header host={host} admin={admin} now={now} />
      </Reveal>
      {charts && (
        <Reveal index={2}>
          <Charts host={host} history={history} />
        </Reveal>
      )}
      {stats && sensors && (
        <Reveal index={3}>
          <Sensors stats={stats} samples={history.samples} />
        </Reveal>
      )}
      {info && storage && (
        <Reveal index={4} className="grid gap-4 lg:grid-cols-3 lg:items-start">
          <Filesystems info={info} />
          <Disks info={info} />
          <Pools info={info} />
        </Reveal>
      )}
      {info && workloads && (
        <Reveal index={5} className="grid gap-4 lg:grid-cols-2 lg:items-start">
          {info.containers && <ContainersPanel containers={info.containers} />}
          {info.services && <ServicesPanel services={info.services} />}
        </Reveal>
      )}
    </>
  );
}

function PageSkeleton() {
  return (
    <Page>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <PanelSkeleton lines={3} />
        <PanelSkeleton lines={3} />
        <PanelSkeleton lines={3} />
      </div>
      <PanelSkeleton lines={3} className="min-h-[180px]" />
      <div className="grid gap-4 lg:grid-cols-2">
        <PanelSkeleton lines={5} />
        <PanelSkeleton lines={5} />
      </div>
    </Page>
  );
}

/**
 * The Hosts page: the machines dash reads over SSH. A grid of every host when there is more than
 * one, then the detail of the host the route names (the first host without one).
 */
export function HostsPage({ host: route }: { host?: string }) {
  const env = useTopic("ssh.hosts");
  const admin = settingsAvailable(useMe().data);
  const now = useNow(30_000).getTime();
  const hosts = env?.data.hosts;
  useFeedHistories(hosts);

  if (!env || !hosts) return <PageSkeleton />;

  if (hosts.length === 0) {
    return (
      <Page>
        <Panel>
          <Empty className="space-y-3">
            <InfoIcon className="mx-auto size-5 text-accent" aria-hidden="true" />
            <p>
              No host is set up yet. dash reads a Linux machine over SSH and installs nothing on it.
            </p>
            {admin && (
              <Link
                href={SETTINGS_PATH}
                className="inline-flex text-accent underline-offset-4 hover:underline"
              >
                Add a host
              </Link>
            )}
          </Empty>
        </Panel>
      </Page>
    );
  }

  const selected = route === undefined ? hosts[0] : findHost(hosts, route);
  return (
    <Page>
      {hosts.length > 1 && (
        <Reveal index={0}>
          <FleetGrid hosts={hosts} selected={selected} admin={admin} />
        </Reveal>
      )}
      {selected ? (
        <HostDetail key={selected.ref} host={selected} admin={admin} now={now} />
      ) : (
        <Reveal index={1}>
          <Panel>
            <Empty>
              No host named <span className="num text-fg">{route}</span>.{" "}
              <Link href="/hosts" className="text-accent underline-offset-4 hover:underline">
                Back to all hosts
              </Link>
            </Empty>
          </Panel>
        </Reveal>
      )}
    </Page>
  );
}
