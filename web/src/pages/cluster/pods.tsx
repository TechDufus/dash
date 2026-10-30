import { useMemo, useState } from "react";
import { Link, useSearchParams } from "wouter";
import type { ClusterData, SourceStatus } from "@/api/types";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { HealthBadge } from "@/components/status";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ageSeconds, formatBytes, formatDuration } from "@/lib/format";
import { compareHealth, filterSearch, formatCores, matchesQuery, podPath, ROW_CAP } from "./common";
import { FilterBar, ShowAll, useClusterFilters } from "./filters";

const TH = "h-9 px-3 text-xs font-medium text-muted";
const TD = "px-3 py-2";

export function PodsTab({
  data,
  sources,
}: {
  data: ClusterData | undefined;
  sources: SourceStatus[] | undefined;
}) {
  const filters = useClusterFilters();
  const [params] = useSearchParams();
  const [all, setAll] = useState(false);
  const { ns, needle, health } = filters;

  const namespaces = useMemo(
    () => [...new Set(data?.pods.map((p) => p.namespace) ?? [])].sort(),
    [data],
  );
  const rows = useMemo(() => {
    if (!data) return [];
    return data.pods
      .filter(
        (p) =>
          (ns === "all" || p.namespace === ns) &&
          (health === "all" || p.health === health) &&
          matchesQuery(needle, p.name, p.namespace, p.node, p.phase, p.reason),
      )
      .sort(
        (a, b) =>
          compareHealth(a.health, b.health) ||
          b.restarts - a.restarts ||
          a.namespace.localeCompare(b.namespace) ||
          a.name.localeCompare(b.name),
      );
  }, [data, ns, needle, health]);

  if (!data) return <PanelSkeleton lines={6} />;
  const shown = all ? rows : rows.slice(0, ROW_CAP);
  // The sheet route keeps the active filters in its query so closing it restores them.
  const search = filterSearch(params);
  const suffix = search ? `?${search}` : "";

  return (
    <Panel>
      <PanelHeader title="Pods" sources={sources} />
      <FilterBar
        filters={filters}
        namespaces={namespaces}
        placeholder="Search pod, namespace, node"
        shown={rows.length}
        total={data.pods.length}
      />
      {rows.length === 0 ? (
        <Empty>No pods match the filters</Empty>
      ) : (
        <Table>
          <TableHeader>
            <TableRow className="border-line hover:bg-transparent">
              <TableHead className={TH}>Health</TableHead>
              <TableHead className={TH}>Pod</TableHead>
              <TableHead className={TH}>Phase</TableHead>
              <TableHead className={`${TH} text-right`}>Ready</TableHead>
              <TableHead className={`${TH} text-right`}>CPU</TableHead>
              <TableHead className={`${TH} text-right`}>Memory</TableHead>
              <TableHead className={`${TH} text-right`}>Restarts</TableHead>
              <TableHead className={TH}>Node</TableHead>
              <TableHead className={`${TH} text-right`}>Age</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {shown.map((p) => (
              <TableRow
                key={`${p.namespace}/${p.name}`}
                className="relative cursor-pointer border-line"
              >
                <TableCell className={TD}>
                  <HealthBadge health={p.health} />
                </TableCell>
                <TableCell className={TD}>
                  {/* The ::after layer stretches the link over the whole row. */}
                  <Link
                    href={`${podPath(p.namespace, p.name)}${suffix}`}
                    className="after:absolute after:inset-0 after:content-['']"
                  >
                    <span className="text-faint">{p.namespace}/</span>
                    <span className="font-medium text-fg">{p.name}</span>
                  </Link>
                </TableCell>
                <TableCell className={TD}>
                  <span className="text-fg">{p.phase}</span>
                  {p.reason && p.reason !== p.phase && (
                    <span className="ml-1.5 text-xs text-muted">{p.reason}</span>
                  )}
                </TableCell>
                <TableCell className={`${TD} num text-right`}>
                  {p.readyContainers}/{p.totalContainers}
                </TableCell>
                <TableCell className={`${TD} num text-right`}>{formatCores(p.cpuCores)}</TableCell>
                <TableCell className={`${TD} num text-right`}>
                  {p.memBytes == null ? "–" : formatBytes(p.memBytes)}
                </TableCell>
                <TableCell
                  className={`${TD} num text-right ${p.restarts > 0 ? "text-fg" : "text-muted"}`}
                >
                  {p.restarts}
                </TableCell>
                <TableCell className={`${TD} text-muted`}>{p.node || "–"}</TableCell>
                <TableCell className={`${TD} num text-right text-muted`}>
                  {formatDuration(ageSeconds(p.createdAt))}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <ShowAll shown={shown.length} total={rows.length} onAll={() => setAll(true)} />
    </Panel>
  );
}
