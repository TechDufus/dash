import { Activity, Clock, Monitor, Server, Thermometer } from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "wouter";
import { BytesBar } from "@/components/bytes-bar";
import { Gauge } from "@/components/gauge";
import { Panel } from "@/components/panel";
import { Spark } from "@/components/spark";
import { HealthBadge, StatusDot } from "@/components/status";
import { formatCelsius, formatDuration, formatNumber, formatPercent } from "@/lib/format";
import { cn } from "@/lib/utils";
import { useHostHistory } from "./history";
import { hostPath, needsSetup, SETTINGS_PATH, stateLabel } from "./hosts";
import type { Host } from "./model.gen";

function Fact({
  icon,
  label,
  value,
  title,
}: {
  icon: ReactNode;
  label: string;
  value: string;
  title?: string;
}) {
  return (
    <div className="flex min-w-0 items-center gap-1.5 text-xs text-muted" title={title}>
      <span className="shrink-0 [&_svg]:size-3.5" aria-hidden="true">
        {icon}
      </span>
      <dt className="shrink-0">{label}</dt>
      <dd className="num ml-auto truncate text-fg">{value}</dd>
    </div>
  );
}

/** The CPU of the last hour as a sparkline, once the host has a few samples. */
function CpuSpark({ name }: { name: string }) {
  const q = useHostHistory(name, true);
  const samples = q.data?.samples ?? [];
  if (samples.length < 2) return null;
  return (
    <div className="-mx-1 -mb-1" title="CPU, last hour">
      <Spark
        points={samples.map((s) => [s.t, s.cpuRatio])}
        format={formatPercent}
        className="h-8"
      />
    </div>
  );
}

/** The link to the settings where an administrator finishes a host's setup or checks it. */
export function SetupLink({ host, className }: { host: Host; className?: string }) {
  if (host.state === "up" || host.state === "connecting") return null;
  return (
    <Link
      href={SETTINGS_PATH}
      className={cn(
        "relative z-10 inline-flex text-xs font-medium text-accent underline-offset-4 hover:underline",
        className,
      )}
    >
      {needsSetup(host.state) ? "Finish setup in settings" : "Check its settings"}{" "}
      <span className="sr-only">for {host.name}</span>
    </Link>
  );
}

function HostCard({ host, selected, admin }: { host: Host; selected: boolean; admin: boolean }) {
  const s = host.state === "up" ? host.stats : undefined;
  const os = host.info?.os ?? "";
  return (
    <Panel
      interactive
      className={cn(
        "relative flex h-full flex-col gap-3 p-4",
        selected && "border-accent/50 bg-accent/5",
        !selected && host.health === "crit" && "border-crit/30",
      )}
    >
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <div className="flex min-w-0 items-center gap-2">
            <StatusDot health={host.health} />
            {/* The whole card is this link (its ::after covers the card); the setup link sits
                above it, so no link is nested in another. */}
            <Link
              href={hostPath(host.name)}
              aria-label={`Host ${host.name}, ${stateLabel(host.state).toLowerCase()}${selected ? ", shown below" : ""}`}
              aria-current={selected ? "true" : undefined}
              className="truncate text-sm font-medium text-fg outline-none after:absolute after:inset-0 after:rounded-2xl focus-visible:after:ring-2 focus-visible:after:ring-accent"
            >
              {host.name}
            </Link>
          </div>
          {os && <div className="truncate text-xs text-muted">{os}</div>}
        </div>
        {selected ? (
          <span className="shrink-0 rounded-full border border-accent/30 bg-accent/10 px-2 py-0.5 text-[11px] font-medium text-accent">
            Shown below
          </span>
        ) : (
          host.state !== "up" && <HealthBadge health={host.health} label={stateLabel(host.state)} />
        )}
      </div>
      {s ? (
        <>
          <div className="flex items-center gap-4">
            <Gauge
              value={s.cpuRatio}
              size={76}
              strokeWidth={7}
              caption="CPU"
              label={`CPU ${formatPercent(s.cpuRatio)}`}
            />
            {/* A zero total is memory the host does not report. */}
            {s.memTotalBytes > 0 && (
              <div className="min-w-0 flex-1">
                <BytesBar used={s.memUsedBytes} total={s.memTotalBytes} label="Memory" />
              </div>
            )}
          </div>
          <CpuSpark name={host.name} />
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
            {s.cpuTempC != null && (
              <Fact icon={<Thermometer />} label="CPU temp" value={formatCelsius(s.cpuTempC)} />
            )}
          </dl>
        </>
      ) : (
        <div className="mt-auto space-y-2 border-t border-line pt-2.5">
          {selected && <HealthBadge health={host.health} label={stateLabel(host.state)} />}
          <p className="text-xs text-muted">{host.message}</p>
          {admin && <SetupLink host={host} />}
        </div>
      )}
    </Panel>
  );
}

/** Every host at a glance, in configuration order; each card leads to its host, the shown one marked. */
export function FleetGrid({
  hosts,
  selected,
  admin,
}: {
  hosts: readonly Host[];
  selected: Host | undefined;
  admin: boolean;
}) {
  const up = hosts.filter((h) => h.state === "up").length;
  return (
    <section aria-labelledby="ssh-fleet" className="space-y-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Server className="size-4 text-accent" aria-hidden="true" />
        <h2 id="ssh-fleet" className="text-sm font-medium tracking-wide text-fg">
          Hosts
        </h2>
        <span className="num inline-flex items-center gap-1 text-xs text-muted">
          <Monitor className="size-3" aria-hidden="true" />
          {up} of {hosts.length} up
        </span>
      </div>
      <ul className="grid grid-cols-[repeat(auto-fill,minmax(260px,1fr))] gap-4">
        {hosts.map((h) => (
          <li key={h.ref}>
            <HostCard host={h} selected={h === selected} admin={admin} />
          </li>
        ))}
      </ul>
    </section>
  );
}
