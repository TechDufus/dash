import { Activity, Clock, Database, HardDrive, Server, Thermometer, Zap } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "wouter";
import type { Health, HostData, SourceStatus } from "@/api/types";
import { BytesBar } from "@/components/bytes-bar";
import { Gauge } from "@/components/gauge";
import { Panel, SourceChips } from "@/components/panel";
import { HealthBadge, StatusDot } from "@/components/status";
import {
  formatCelsius,
  formatDuration,
  formatNumber,
  formatPercent,
  formatWatts,
} from "@/lib/format";
import { HEALTH_LABEL, HEALTH_TEXT, worstHealth } from "@/lib/health";
import { cn } from "@/lib/utils";
import type { Machine } from "./model.gen";

/** The hottest CPU package, or undefined when the host reports none. */
export function maxPackageTemp(host: HostData): number | undefined {
  return host.packageTempsC.length > 0 ? Math.max(...host.packageTempsC) : undefined;
}

export function poolHealth(state: string): Health {
  const s = state.toUpperCase();
  if (s === "ONLINE") return "ok";
  if (s === "DEGRADED") return "warn";
  return "crit";
}

/**
 * The pools in a few words: "3 online", or how many are not and the state of the worst
 * ("1 of 3 degraded"; "2 of 3 not online" when several are), coloured by that worst health.
 */
function poolSummary(host: HostData): { text: string; health: Health } {
  const n = host.pools.length;
  const bad = host.pools.filter((p) => poolHealth(p.state) !== "ok");
  const health = worstHealth(bad.map((p) => poolHealth(p.state)));
  if (bad.length === 0) return { text: `${n} online`, health };
  const worst = bad.find((p) => poolHealth(p.state) === health) ?? bad[0];
  const state = bad.length === 1 && worst ? worst.state.toLowerCase() : "not online";
  return { text: `${bad.length} of ${n} ${state}`, health };
}

function Fact({
  icon,
  label,
  value,
  className,
  title,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  className?: string;
  title?: string;
}) {
  return (
    <div className="flex min-w-0 items-center gap-1.5 text-xs text-muted" title={title}>
      <span className="shrink-0 [&_svg]:size-3.5" aria-hidden="true">
        {icon}
      </span>
      <dt className="shrink-0">{label}</dt>
      <dd className={cn("num ml-auto truncate text-fg", className)}>{value}</dd>
    </div>
  );
}

function HostCard({ host, selected }: { host: HostData; selected: boolean }) {
  const health = host.health;
  const pkg = maxPackageTemp(host);
  const pools = host.pools.length > 0 ? poolSummary(host) : undefined;
  const failing = host.disks.filter((d) => !d.healthy).length;
  return (
    <Link
      href={`/hardware/${encodeURIComponent(host.name)}`}
      aria-label={`Host ${host.name}, ${HEALTH_LABEL[health]}${selected ? ", shown below" : ""}`}
      aria-current={selected ? "true" : undefined}
      className="block h-full rounded-2xl"
    >
      <Panel
        interactive
        className={cn(
          "flex h-full flex-col gap-3 p-4",
          selected && "border-accent/50 bg-accent/5",
          !selected && health === "crit" && "border-crit/30",
        )}
      >
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="flex min-w-0 items-center gap-2">
              <StatusDot health={health} />
              <span className="truncate text-sm font-medium text-fg">{host.name}</span>
            </div>
            <div className="truncate text-xs text-muted">{host.model || "Host"}</div>
          </div>
          {selected && (
            <span className="shrink-0 rounded-full border border-accent/30 bg-accent/10 px-2 py-0.5 text-[11px] font-medium text-accent">
              Shown below
            </span>
          )}
        </div>
        <div className="flex items-center gap-4">
          <Gauge
            value={host.cpuRatio}
            size={76}
            strokeWidth={7}
            caption="CPU"
            label={`CPU ${formatPercent(host.cpuRatio)}`}
          />
          {/* A zero total is memory the host's exporter does not report. */}
          {host.memTotalBytes > 0 && (
            <div className="min-w-0 flex-1">
              <BytesBar used={host.memUsedBytes} total={host.memTotalBytes} label="Memory" />
            </div>
          )}
        </div>
        <dl className="mt-auto grid grid-cols-2 gap-x-4 gap-y-2 border-t border-line pt-2.5">
          <Fact icon={<Zap />} label="Power" value={formatWatts(host.powerWatts)} />
          <Fact
            icon={<Thermometer />}
            label="CPU temp"
            value={formatCelsius(pkg)}
            className={pkg === undefined ? undefined : HEALTH_TEXT[host.cpuTempHealth]}
          />
          {pools && (
            <Fact
              icon={<Database />}
              label="Pools"
              value={pools.text}
              className={pools.health === "ok" ? undefined : HEALTH_TEXT[pools.health]}
            />
          )}
          {host.disks.length > 0 && (
            <Fact
              icon={<HardDrive />}
              label="Disks"
              value={
                failing === 0
                  ? `${host.disks.length} healthy`
                  : `${failing} of ${host.disks.length} failing`
              }
              className={failing === 0 ? undefined : "text-crit"}
            />
          )}
        </dl>
      </Panel>
    </Link>
  );
}

function MachineCard({ machine }: { machine: Machine }) {
  const s = machine.stats;
  const health: Health = machine.up ? "ok" : "crit";
  const state = machine.up ? "Up" : "Down";
  // OS and arch come from node_uname_info, which a machine may not export even when its other
  // stats arrive; the source then names it, as it does for a machine without stats.
  const platform = s ? [s.os, s.arch].filter((p) => p !== "").join(" · ") : "";
  return (
    <Panel
      role="group"
      aria-label={`Machine ${machine.name}, ${state.toLowerCase()}`}
      className={cn("flex h-full flex-col gap-3 p-4", !machine.up && "border-crit/30")}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <StatusDot health={health} />
            <span className="truncate text-sm font-medium text-fg">{machine.name}</span>
          </div>
          {platform ? (
            <div className="num truncate text-xs text-muted" title={s?.kernel || undefined}>
              {platform}
            </div>
          ) : (
            <div className="num truncate text-xs text-muted" title={machine.source}>
              {machine.source}
            </div>
          )}
        </div>
        <HealthBadge health={health} label={state} />
      </div>
      {/* A zero total, CPU count or uptime is the plugin's "not reported" (a machine whose
          node-exporter lacks that collector), so the figure is left out rather than drawn as 0. */}
      {s && (
        <>
          {(s.cpus > 0 || s.memTotalBytes > 0 || s.diskTotalBytes > 0) && (
            <div className="flex items-center gap-4">
              {s.cpus > 0 && (
                <div className="flex shrink-0 flex-col items-center gap-0.5">
                  <Gauge
                    value={s.cpuRatio}
                    size={76}
                    strokeWidth={7}
                    caption="CPU"
                    label={`CPU ${formatPercent(s.cpuRatio)}`}
                  />
                  <span className="num text-[11px] text-muted">
                    {formatNumber(s.cpus)} {s.cpus === 1 ? "core" : "cores"}
                  </span>
                </div>
              )}
              <div className="min-w-0 flex-1 space-y-3">
                {s.memTotalBytes > 0 && (
                  <BytesBar used={s.memUsedBytes} total={s.memTotalBytes} label="Memory" />
                )}
                {s.diskTotalBytes > 0 && (
                  <BytesBar used={s.diskUsedBytes} total={s.diskTotalBytes} label="Root disk" />
                )}
              </div>
            </div>
          )}
          <dl className="mt-auto space-y-1.5 border-t border-line pt-2.5">
            <Fact
              icon={<Activity />}
              label="Load"
              title="Load average over 1, 5 and 15 minutes"
              value={[s.load1, s.load5, s.load15].map((l) => formatNumber(l, 2)).join(" · ")}
            />
            {s.uptimeSeconds > 0 && (
              <Fact icon={<Clock />} label="Uptime" value={formatDuration(s.uptimeSeconds)} />
            )}
            {s.tempC != null && (
              <Fact icon={<Thermometer />} label="Temperature" value={formatCelsius(s.tempC)} />
            )}
          </dl>
        </>
      )}
    </Panel>
  );
}

/**
 * Every host and machine at a glance: hosts first (each a link to its detail, the shown one
 * marked), then the machines, both in configuration order. The machines' source chips sit in the
 * heading, since the hosts' own are on the detail's title panel.
 */
export function FleetGrid({
  hosts,
  selected,
  machines,
  sources,
}: {
  hosts: HostData[];
  selected: HostData | undefined;
  machines: Machine[];
  sources: SourceStatus[] | undefined;
}) {
  return (
    <section aria-labelledby="hardware-fleet" className="space-y-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Server className="size-4 text-accent" aria-hidden="true" />
        <h2 id="hardware-fleet" className="text-sm font-medium tracking-wide text-fg">
          Fleet
        </h2>
        <SourceChips sources={sources} />
      </div>
      <ul className="grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-4">
        {hosts.map((h) => (
          <li key={h.ref}>
            <HostCard host={h} selected={h === selected} />
          </li>
        ))}
        {machines.map((m) => (
          <li key={m.ref}>
            <MachineCard machine={m} />
          </li>
        ))}
      </ul>
    </section>
  );
}
