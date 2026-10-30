import type { ClusterData } from "@/api/types";
import { Gauge } from "@/components/gauge";
import { LinkButtons } from "@/components/links";
import { Empty, Panel, PanelSkeleton } from "@/components/panel";
import { SparkSeries } from "@/components/spark";
import { HealthBadge } from "@/components/status";
import { formatPercent } from "@/lib/format";

export function NodesTab({ data }: { data: ClusterData | undefined }) {
  if (!data) {
    return (
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <PanelSkeleton lines={4} />
        <PanelSkeleton lines={4} />
        <PanelSkeleton lines={4} />
      </div>
    );
  }
  if (data.nodes.length === 0) {
    return (
      <Panel>
        <Empty>No nodes reported</Empty>
      </Panel>
    );
  }
  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      {data.nodes.map((node) => (
        <Panel key={node.name} className="space-y-4 p-4">
          <div className="flex flex-wrap items-start justify-between gap-2">
            <div className="min-w-0">
              <h3 className="truncate text-sm font-medium text-fg">{node.name}</h3>
              <div className="num text-xs text-muted">{node.ip}</div>
            </div>
            <HealthBadge
              health={node.ready ? "ok" : "crit"}
              label={node.ready ? "Ready" : "NotReady"}
            />
          </div>

          <dl className="flex gap-6 text-xs">
            <div>
              <dt className="text-muted">Kubelet</dt>
              <dd className="num text-fg">{node.kubeletVersion}</dd>
            </div>
            <div>
              <dt className="text-muted">Pods</dt>
              <dd className="num text-fg">{node.pods}</dd>
            </div>
          </dl>

          <div className="grid grid-cols-2 gap-4">
            <div className="flex flex-col items-center gap-2">
              <Gauge
                value={node.cpuRatio}
                size={96}
                caption="CPU"
                label={`CPU ${formatPercent(node.cpuRatio)} on ${node.name}`}
              />
              <SparkSeries
                name="node.cpu"
                range="6h"
                params={{ node: node.name }}
                format={formatPercent}
                className="w-full"
              />
            </div>
            <div className="flex flex-col items-center gap-2">
              <Gauge
                value={node.memRatio}
                size={96}
                caption="Memory"
                label={`Memory ${formatPercent(node.memRatio)} on ${node.name}`}
              />
              <SparkSeries
                name="node.mem"
                range="6h"
                params={{ node: node.name }}
                format={formatPercent}
                className="w-full"
              />
            </div>
          </div>

          <LinkButtons links={node.links} />
        </Panel>
      ))}
    </div>
  );
}
