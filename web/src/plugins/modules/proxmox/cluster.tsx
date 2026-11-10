import { Clock, Cpu, HardDrive, Network, Server } from "lucide-react";
import type { ReactNode } from "react";
import type { Health, VM } from "@/api/types";
import { BytesBar } from "@/components/bytes-bar";
import { Gauge } from "@/components/gauge";
import { Reveal } from "@/components/page";
import { Panel, PanelHeader } from "@/components/panel";
import { HealthBadge, StatusDot } from "@/components/status";
import { formatDuration, formatNumber } from "@/lib/format";
import { cn } from "@/lib/utils";
import type { NodeData, PVEClusterData, PVEStorageData } from "./model.gen";

/** The node a guest runs on, as a chip beside its other chips. */
export function NodeChip({ node, className }: { node: string; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border border-accent/25 bg-accent/10 px-2 py-0.5 text-[11px] font-medium text-accent",
        className,
      )}
      title={`Runs on node ${node}`}
    >
      <Server className="size-3" aria-hidden="true" />
      <span className="sr-only">Node </span>
      {node}
    </span>
  );
}

function threadsLabel(n: number): string {
  return `${formatNumber(n)} ${n === 1 ? "thread" : "threads"}`;
}

function clusterTitle(cluster: PVEClusterData): string {
  if (!cluster.standalone) return cluster.name || "Proxmox cluster";
  const node = cluster.nodes[0]?.node;
  return node ? `Standalone server ${node}` : "Standalone server";
}

/**
 * The cluster as one machine: its quorum, how many nodes answer, and the CPU, memory and storage
 * of the online nodes added up. A standalone server has no quorum to lose, so it shows no badge;
 * a cluster whose quorum dash cannot read makes no claim about it either way.
 */
export function ClusterSummary({
  cluster,
  storage,
}: {
  cluster: PVEClusterData;
  storage?: PVEStorageData;
}) {
  const { totals } = cluster;
  // An unknown node is not known to be down, so only an offline one makes the count critical.
  const nodesHealth: Health = cluster.nodes.some((n) => n.status === "offline")
    ? "crit"
    : totals.nodesOnline === totals.nodes
      ? "ok"
      : "unknown";
  const quorumLost = !cluster.standalone && cluster.quorumKnown && !cluster.quorate;
  return (
    <Panel>
      <PanelHeader title={clusterTitle(cluster)} icon={<Network />}>
        {!cluster.standalone &&
          (!cluster.quorumKnown ? (
            <HealthBadge health="unknown" label="Quorum unknown" />
          ) : cluster.quorate ? (
            <HealthBadge health="ok" label="Quorate" />
          ) : (
            <HealthBadge health="crit" label="Quorum lost" />
          ))}
        <HealthBadge
          health={nodesHealth}
          label={`${totals.nodesOnline} of ${totals.nodes} ${totals.nodes === 1 ? "node" : "nodes"} online`}
        />
      </PanelHeader>
      {quorumLost && (
        <p className="mx-4 mb-3 rounded-lg border border-crit/30 bg-crit/10 px-3 py-2 text-xs text-crit">
          VMs keep running; nothing can be started, stopped or changed until quorum returns.
        </p>
      )}
      <div className="flex flex-wrap items-center gap-x-6 gap-y-4 px-4 pb-4">
        <div className="flex shrink-0 flex-col items-center gap-0.5">
          <Gauge
            value={totals.cpuRatio}
            size={76}
            strokeWidth={7}
            caption="CPU"
            label={`CPU ${Math.round(totals.cpuRatio * 100)}% of ${threadsLabel(totals.threads)}`}
          />
          <span className="num text-[11px] text-muted">{threadsLabel(totals.threads)}</span>
        </div>
        <div className="grid min-w-[220px] flex-1 gap-3 sm:grid-cols-2">
          <BytesBar used={totals.memUsedBytes} total={totals.memTotalBytes} label="Memory" />
          {storage && (
            <BytesBar used={storage.usedBytes} total={storage.sizeBytes} label="Storage" />
          )}
        </div>
      </div>
    </Panel>
  );
}

function Fact({ icon, label, value }: { icon: ReactNode; label: string; value: string }) {
  return (
    <div className="flex min-w-0 items-center gap-1.5 text-xs text-muted">
      <span className="shrink-0 [&_svg]:size-3.5">{icon}</span>
      <span className="shrink-0">{label}</span>
      <span className="num ml-auto truncate text-fg" title={value}>
        {value}
      </span>
    </div>
  );
}

/**
 * One node of the cluster. A node that is not online has no usage to show; its facts are the
 * last it reported, so they stay. An offline node is down; an unknown one has only gone quiet,
 * so it is shown neutrally rather than as healthy or as lost.
 */
function NodeCard({
  node,
  guests,
}: {
  node: NodeData;
  /** The node's guests and how many of them run; undefined until the VM list has arrived. */
  guests?: { total: number; running: number };
}) {
  const online = node.status === "online";
  const health: Health = online ? "ok" : node.status === "offline" ? "crit" : "unknown";
  return (
    <Panel
      className={cn("flex h-full flex-col gap-3 p-4", health === "crit" && "border-crit/30")}
      aria-label={`Node ${node.node}, ${node.status}`}
      role="group"
    >
      <div className="flex items-start justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <StatusDot health={health} />
          <span className="truncate text-sm font-medium text-fg">{node.node}</span>
        </div>
        <HealthBadge health={health} label={node.status} />
      </div>

      <div className="flex items-center gap-4">
        <div className="flex shrink-0 flex-col items-center gap-0.5">
          <Gauge
            value={online ? node.cpuRatio : 0}
            display={online ? undefined : "–"}
            size={76}
            strokeWidth={7}
            caption="CPU"
          />
          <span className="num text-[11px] text-muted">
            {node.threads > 0 ? threadsLabel(node.threads) : "–"}
          </span>
        </div>
        <div className="min-w-0 flex-1 space-y-3">
          <BytesBar
            used={online ? node.memUsedBytes : undefined}
            total={online ? node.memTotalBytes : undefined}
            label="Memory"
          />
          <Fact
            icon={<Clock />}
            label="Uptime"
            value={online ? formatDuration(node.uptimeSeconds) : "–"}
          />
        </div>
      </div>

      <div className="mt-auto space-y-1.5 border-t border-line pt-2.5">
        <Fact icon={<HardDrive />} label="Proxmox VE" value={node.version || "–"} />
        <Fact icon={<Cpu />} label="CPU" value={node.cpuModel || "–"} />
        <Fact icon={<Server />} label="Guests" value={guestsLabel(online, guests)} />
      </div>
    </Panel>
  );
}

/**
 * A node's guest count. Before the VM list arrives there is nothing to count. A node that is not
 * online keeps reporting its guests as running for minutes after it drops, so only an online
 * node's running count is believed.
 */
function guestsLabel(online: boolean, guests?: { total: number; running: number }): string {
  if (!guests) return "–";
  if (guests.total === 0) return "none";
  if (!online) return `${guests.total} ${guests.total === 1 ? "guest" : "guests"}`;
  return `${guests.running} running of ${guests.total}`;
}

/**
 * One card per node, each counting the guests the VM list places on it (templates left out).
 * `vms` is undefined until the VM list has arrived, so the cards do not claim a node has none.
 */
export function NodeCards({
  nodes,
  vms,
  revealFrom,
}: {
  nodes: NodeData[];
  vms: VM[] | undefined;
  revealFrom: number;
}) {
  const counts = new Map<string, { total: number; running: number }>();
  for (const vm of vms ?? []) {
    const c = counts.get(vm.node) ?? { total: 0, running: 0 };
    c.total++;
    if (vm.status === "running") c.running++;
    counts.set(vm.node, c);
  }
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-4">
      {nodes.map((n, i) => (
        <Reveal key={n.ref} index={revealFrom + i} className="h-full">
          <NodeCard
            node={n}
            guests={vms ? (counts.get(n.node) ?? { total: 0, running: 0 }) : undefined}
          />
        </Reveal>
      ))}
    </div>
  );
}
