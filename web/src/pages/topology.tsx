import { Network, Server } from "lucide-react";
import { useReducedMotion } from "motion/react";
import { type RefObject, useCallback, useMemo, useRef, useState } from "react";
import type {
  Health,
  SourceStatus,
  Topology,
  TopologyGroup,
  TopologyHost,
  TopologyVM,
} from "@/api/types";
import { BytesBar, PVE_MEM_TIP } from "@/components/bytes-bar";
import { Gauge } from "@/components/gauge";
import { AnimatedBeam } from "@/components/magicui/animated-beam";
import { Panel, PanelHeader } from "@/components/panel";
import { HealthBadge, StatusDot } from "@/components/status";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { formatCelsius, formatPercent, formatWatts } from "@/lib/format";
import { HEALTH_TEXT, ratioHealth } from "@/lib/health";
import { useIsDesktop } from "@/lib/hooks";
import { cn } from "@/lib/utils";

const ACCENT_HEX = "#38e1ff";
const HEALTH_HEX: Record<Health, string> = {
  ok: "#3ee69a",
  warn: "#ffb547",
  crit: "#ff4d6d",
  unknown: "#8a93a6",
};
const VISIBLE_NAMESPACES = 5;

type CardRef = RefObject<HTMLDivElement | null>;

/** Stable ref objects per key, so beams can point at cards before they mount. */
function useRefRegistry(): (key: string) => CardRef {
  const map = useRef(new Map<string, CardRef>());
  return useCallback((key: string) => {
    let r = map.current.get(key);
    if (!r) {
      r = { current: null };
      map.current.set(key, r);
    }
    return r;
  }, []);
}

/** Svg-level opacity bucket for the travelling gradient (Tailwind needs static class names). */
function gradientOpacityClass(strength: number): string {
  if (strength < 0.25) return "opacity-40";
  if (strength < 0.5) return "opacity-60";
  if (strength < 0.75) return "opacity-80";
  return "opacity-100";
}

/**
 * A host only Proxmox describes (no hardware plugin for its machine) reports no power or
 * temperature, and no memory once its node is not online: a zero reading is no reading, shown as
 * a dash, never as 0 W, 0 °C or 0 %.
 */
function reading(value: number, format: (v: number) => string): string {
  return value > 0 ? format(value) : "–";
}

function HostCard({ host, className }: { host: TopologyHost; className?: string }) {
  const memRatio = host.memUsedRatio > 0 ? host.memUsedRatio : undefined;
  return (
    <Panel className={cn("relative z-10 space-y-3 p-3", className)}>
      <div className="flex items-center gap-2">
        <Server className="size-4 text-accent" aria-hidden="true" />
        <span className="truncate text-sm font-medium text-fg">{host.name}</span>
        <StatusDot health={host.health} className="ml-auto" />
      </div>
      <dl className="grid grid-cols-2 gap-2 text-xs">
        <div>
          <dt className="text-muted">Power</dt>
          <dd className="num text-fg">{reading(host.powerWatts, formatWatts)}</dd>
        </div>
        <div>
          <dt className="text-muted">CPU temp</dt>
          <dd className={cn("num", HEALTH_TEXT[host.cpuTempHealth])}>
            {reading(host.cpuTempC, formatCelsius)}
          </dd>
        </div>
      </dl>
      <div>
        <div className="mb-1 flex items-baseline justify-between text-xs">
          <span className="text-muted">Memory</span>
          <span className={cn("num", HEALTH_TEXT[ratioHealth(memRatio)])}>
            {reading(host.memUsedRatio, formatPercent)}
          </span>
        </div>
        <BytesBar used={memRatio} total={1} compact label="Host memory" />
      </div>
    </Panel>
  );
}

function VMCard({ vm, className }: { vm: TopologyVM; className?: string }) {
  return (
    <Panel className={cn("relative z-10 p-3", className)}>
      <div className="flex items-center gap-2">
        <StatusDot health={vm.running ? vm.health : "unknown"} />
        <span className="truncate text-sm font-medium text-fg">{vm.name}</span>
        <span className="num ml-auto shrink-0 text-[11px] text-faint">{vm.vmid}</span>
      </div>
      {vm.running ? (
        <div className="mt-2 flex items-center gap-3">
          <Gauge
            value={vm.cpuRatio}
            size={52}
            strokeWidth={5}
            caption="CPU"
            label={`${vm.name} CPU ${formatPercent(vm.cpuRatio)}`}
          />
          <div className="min-w-0 flex-1">
            <div className="mb-1 flex items-baseline justify-between text-[11px]">
              <Tooltip>
                <TooltipTrigger asChild>
                  <span className="cursor-help text-muted underline decoration-dotted underline-offset-2">
                    Mem
                  </span>
                </TooltipTrigger>
                <TooltipContent>{PVE_MEM_TIP}</TooltipContent>
              </Tooltip>
              <span className="num text-fg">{formatPercent(vm.memRatio)}</span>
            </div>
            <BytesBar used={vm.memRatio} total={1} compact neutral label={`${vm.name} memory`} />
          </div>
        </div>
      ) : (
        <p className="mt-2 text-xs font-medium tracking-wide text-muted uppercase">stopped</p>
      )}
    </Panel>
  );
}

function GroupCard({ group, className }: { group: TopologyGroup; className?: string }) {
  const [expanded, setExpanded] = useState(false);
  const total = group.namespaces.length;
  const hidden = Math.max(0, total - VISIBLE_NAMESPACES);
  const shown = expanded ? group.namespaces : group.namespaces.slice(0, VISIBLE_NAMESPACES);
  const pods = group.namespaces.reduce((n, ns) => n + ns.pods, 0);
  return (
    <Panel className={cn("relative z-10 p-3", className)}>
      <div className="flex items-center gap-2">
        <span className="text-sm font-medium text-fg">{group.name}</span>
        <span className="num text-[11px] text-faint">{pods} pods</span>
        <HealthBadge health={group.health} className="ml-auto" />
      </div>
      {total === 0 ? (
        <p className="mt-2 text-xs text-muted">No namespaces</p>
      ) : (
        <ul className="mt-2 space-y-1">
          {shown.map((ns) => (
            <li key={ns.name} className="flex items-center gap-2 text-xs">
              <StatusDot health={ns.health} />
              <span className="truncate text-muted">{ns.name}</span>
              <span className="num ml-auto shrink-0 text-fg">{ns.pods}</span>
            </li>
          ))}
        </ul>
      )}
      {hidden > 0 && (
        <button
          type="button"
          onClick={() => setExpanded((e) => !e)}
          aria-expanded={expanded}
          className="mt-2 rounded text-xs text-accent hover:underline"
        >
          {expanded ? "Show less" : `+${hidden} more`}
        </button>
      )}
    </Panel>
  );
}

/** Share of a group's pods that run on one node (0..1). */
function nodeShare(group: TopologyGroup, node: string): number {
  const all = Object.values(group.podsByNode).reduce((n, v) => n + v, 0);
  if (all === 0) return 0;
  return (group.podsByNode[node] ?? 0) / all;
}

function hostKey(host: TopologyHost): string {
  return `host:${host.node}:${host.name}`;
}

interface PlacedVM {
  vm: TopologyVM;
  /** The ref key of the host the VM runs on; undefined when no host is its node. */
  host?: string;
  /** Its place among its host's VMs, so a host's beams fan out in card order. */
  slot: number;
  count: number;
}

/**
 * Each VM with the host whose node it runs on, ordered host by host (in the hosts' order) so the
 * beams of one host do not cross another's. VMs on a node no host stands for come last.
 */
function placeVMs(topology: Topology): PlacedVM[] {
  const hosts = topology.hosts.filter((h) => h.node !== "");
  const byHost = new Map<number, TopologyVM[]>();
  for (const vm of topology.vms) {
    const hi = hosts.findIndex((h) => h.node === vm.node);
    const at = hi === -1 ? hosts.length : hi;
    byHost.set(at, [...(byHost.get(at) ?? []), vm]);
  }
  const placed: PlacedVM[] = [];
  for (let hi = 0; hi <= hosts.length; hi++) {
    const vms = byHost.get(hi) ?? [];
    const host = hosts[hi] ? hostKey(hosts[hi]) : undefined;
    vms.forEach((vm, slot) => {
      placed.push({ vm, host, slot, count: vms.length });
    });
  }
  return placed;
}

function Beams({
  topology,
  placed,
  container,
  getRef: ref,
}: {
  topology: Topology;
  placed: PlacedVM[];
  container: RefObject<HTMLDivElement | null>;
  getRef: (key: string) => CardRef;
}) {
  const reduced = useReducedMotion();
  const animated = !reduced;
  const { groups } = topology;
  const vms = placed.map((p) => p.vm);
  // Kubernetes beams: each VM fans out to the groups it runs pods of, each group fans in from
  // those VMs. Slots follow the cards' top-to-bottom order, so the fans do not cross at a card.
  const links = vms
    .filter((vm) => vm.isK8sNode && vm.running)
    .flatMap((vm, vi) =>
      groups.flatMap((group, gi) => {
        const share = nodeShare(group, vm.name);
        return share > 0 ? [{ vm, vi, group, gi, share }] : [];
      }),
    );
  return (
    <>
      {placed.map(({ vm, host, slot, count }, vi) => {
        if (!host) return null;
        return vm.running ? (
          <AnimatedBeam
            key={`host-${vm.vmid}`}
            containerRef={container}
            fromRef={ref(host)}
            toRef={ref(`vm:${vm.vmid}`)}
            fromSlot={{ index: slot, count }}
            pathColor={ACCENT_HEX}
            pathOpacity={0.25}
            gradientStartColor={ACCENT_HEX}
            gradientStopColor={ACCENT_HEX}
            duration={4}
            delay={vi * 0.3}
            animated={animated}
          />
        ) : (
          <AnimatedBeam
            key={`host-${vm.vmid}`}
            containerRef={container}
            fromRef={ref(host)}
            toRef={ref(`vm:${vm.vmid}`)}
            fromSlot={{ index: slot, count }}
            pathColor={ACCENT_HEX}
            pathOpacity={0.25}
            animated={false}
            dashed
          />
        );
      })}
      {links.map(({ vm, vi, group, gi, share }) => {
        const out = links.filter((l) => l.vm === vm);
        const into = links.filter((l) => l.group === group);
        const strength = Math.max(0.15, share);
        const color = HEALTH_HEX[group.health];
        return (
          <AnimatedBeam
            key={`${vm.vmid}-${group.name}`}
            className={gradientOpacityClass(strength)}
            containerRef={container}
            fromRef={ref(`vm:${vm.vmid}`)}
            toRef={ref(`group:${group.name}`)}
            fromSlot={{ index: out.findIndex((l) => l.group === group), count: out.length }}
            toSlot={{ index: into.findIndex((l) => l.vm === vm), count: into.length }}
            pathColor={color}
            pathOpacity={strength * 0.5}
            gradientStartColor={color}
            gradientStopColor={color}
            duration={5 + gi * 0.5}
            delay={vi * 0.4 + gi * 0.2}
            animated={animated}
          />
        );
      })}
    </>
  );
}

/** One row per host with the VMs on it below, then the VMs no host stands for, then the groups. */
function CompactTopology({ topology, placed }: { topology: Topology; placed: PlacedVM[] }) {
  return (
    <div className="space-y-2 px-4 pb-4">
      {topology.hosts.map((h) => {
        const key = hostKey(h);
        return (
          <div key={key} className="space-y-2">
            <HostRow host={h} />
            {h.node !== "" &&
              placed.filter((p) => p.host === key).map((p) => <VMRow key={p.vm.vmid} vm={p.vm} />)}
          </div>
        );
      })}
      {placed
        .filter((p) => !p.host)
        .map((p) => (
          <VMRow key={p.vm.vmid} vm={p.vm} />
        ))}
      {topology.groups.map((g) => (
        <GroupCard key={g.name} group={g} className="z-0" />
      ))}
    </div>
  );
}

function HostRow({ host }: { host: TopologyHost }) {
  return (
    <Panel className="flex flex-wrap items-center gap-x-3 gap-y-1 p-3 text-xs">
      <Server className="size-4 text-accent" aria-hidden="true" />
      <span className="text-sm font-medium text-fg">{host.name}</span>
      <StatusDot health={host.health} />
      <span className="num ml-auto text-fg">{reading(host.powerWatts, formatWatts)}</span>
      <span className={cn("num", HEALTH_TEXT[host.cpuTempHealth])}>
        {reading(host.cpuTempC, formatCelsius)}
      </span>
      <span className="num text-muted">mem {reading(host.memUsedRatio, formatPercent)}</span>
    </Panel>
  );
}

function VMRow({ vm }: { vm: TopologyVM }) {
  return (
    <Panel className={cn("flex items-center gap-3 p-3 text-xs", !vm.running && "opacity-60")}>
      <StatusDot health={vm.running ? vm.health : "unknown"} />
      <span className="truncate text-sm font-medium text-fg">{vm.name}</span>
      <span className="num text-faint">{vm.vmid}</span>
      {vm.running ? (
        <span className="num ml-auto flex shrink-0 gap-3 text-muted">
          <span>
            cpu{" "}
            <span className={HEALTH_TEXT[ratioHealth(vm.cpuRatio)]}>
              {formatPercent(vm.cpuRatio)}
            </span>
          </span>
          <span>
            mem <span className="text-fg">{formatPercent(vm.memRatio)}</span>
          </span>
        </span>
      ) : (
        <span className="ml-auto font-medium tracking-wide text-muted uppercase">stopped</span>
      )}
    </Panel>
  );
}

interface TopologyPanelProps {
  topology: Topology;
  sources?: SourceStatus[];
  className?: string;
}

/**
 * Hosts -> VMs -> workload groups, joined by animated beams (list without beams below 768 px).
 * Each VM's beam starts at the host of its node. The beams run in the gutters on a layer below
 * the cards, and the cards have an opaque base, so no beam is drawn over or seen through a card.
 */
export function TopologyPanel({ topology, sources, className }: TopologyPanelProps) {
  const isDesktop = useIsDesktop();
  const container = useRef<HTMLDivElement | null>(null);
  const ref = useRefRegistry();
  const placed = useMemo(() => placeVMs(topology), [topology]);
  const card = "relative rounded-2xl bg-panel-solid";
  return (
    <Panel className={className}>
      <PanelHeader title="Topology" icon={<Network aria-hidden="true" />} sources={sources} />
      {isDesktop ? (
        <div ref={container} className="relative px-4 pb-4">
          <div className="pointer-events-none absolute inset-0 z-0">
            <Beams topology={topology} placed={placed} container={container} getRef={ref} />
          </div>
          <div className="relative z-10 grid grid-cols-3 gap-x-12 2xl:gap-x-16">
            <div className="flex flex-col justify-center gap-3">
              {topology.hosts.map((h) => (
                <div key={hostKey(h)} ref={ref(hostKey(h))} className={card}>
                  <HostCard host={h} className="z-auto" />
                </div>
              ))}
            </div>
            <div className="flex flex-col justify-center gap-3">
              {placed.map(({ vm }) => (
                <div
                  key={vm.vmid}
                  ref={ref(`vm:${vm.vmid}`)}
                  // Dim the opaque base too, or a stopped card looks almost like a running one.
                  className={cn(card, !vm.running && "opacity-60")}
                >
                  <VMCard vm={vm} className="z-auto" />
                </div>
              ))}
            </div>
            <div className="flex flex-col justify-center gap-3">
              {topology.groups.map((g) => (
                <div key={g.name} ref={ref(`group:${g.name}`)} className={card}>
                  <GroupCard group={g} className="z-auto" />
                </div>
              ))}
            </div>
          </div>
        </div>
      ) : (
        <CompactTopology topology={topology} placed={placed} />
      )}
    </Panel>
  );
}
