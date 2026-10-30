import { Lock, RotateCw } from "lucide-react";
import { useMemo, useState } from "react";
import type { ClusterData, SourceStatus } from "@/api/types";
import { ui } from "@/api/ui-store";
import { HeadlampIcon } from "@/components/links";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { HealthBadge } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { shortImage } from "@/lib/format";
import { compareHealth, matchesQuery, ROW_CAP } from "./common";
import { FilterBar, ShowAll, useClusterFilters } from "./filters";

const TH = "h-9 px-3 text-xs font-medium text-muted";
const TD = "px-3 py-2";

export function WorkloadsTab({
  data,
  sources,
}: {
  data: ClusterData | undefined;
  sources: SourceStatus[] | undefined;
}) {
  const filters = useClusterFilters();
  const [all, setAll] = useState(false);
  const { ns, needle, health } = filters;

  const namespaces = useMemo(
    () => [...new Set(data?.workloads.map((w) => w.namespace) ?? [])].sort(),
    [data],
  );
  const rows = useMemo(() => {
    if (!data) return [];
    return data.workloads
      .filter(
        (w) =>
          (ns === "all" || w.namespace === ns) &&
          (health === "all" || w.health === health) &&
          matchesQuery(needle, w.name, w.namespace, w.kind, ...w.images),
      )
      .sort(
        (a, b) =>
          compareHealth(a.health, b.health) ||
          a.namespace.localeCompare(b.namespace) ||
          a.name.localeCompare(b.name),
      );
  }, [data, ns, needle, health]);

  if (!data) return <PanelSkeleton lines={6} />;
  const shown = all ? rows : rows.slice(0, ROW_CAP);

  return (
    <Panel>
      <PanelHeader title="Workloads" sources={sources} />
      <FilterBar
        filters={filters}
        namespaces={namespaces}
        placeholder="Search name, namespace, image"
        shown={rows.length}
        total={data.workloads.length}
      />
      {rows.length === 0 ? (
        <Empty>No workloads match the filters</Empty>
      ) : (
        <Table>
          <TableHeader>
            <TableRow className="border-line hover:bg-transparent">
              <TableHead className={TH}>Health</TableHead>
              <TableHead className={TH}>Kind</TableHead>
              <TableHead className={TH}>Workload</TableHead>
              <TableHead className={`${TH} text-right`}>Ready</TableHead>
              <TableHead className={TH}>Images</TableHead>
              <TableHead className={TH}>
                <span className="sr-only">Protected</span>
              </TableHead>
              <TableHead className={`${TH} text-right`}>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {shown.map((w) => {
              const images = w.images.map(shortImage).join(", ");
              return (
                <TableRow key={`${w.kind}/${w.namespace}/${w.name}`} className="border-line">
                  <TableCell className={TD}>
                    <HealthBadge health={w.health} />
                  </TableCell>
                  <TableCell className={`${TD} text-muted`}>{w.kind}</TableCell>
                  <TableCell className={TD}>
                    <span className="text-faint">{w.namespace}/</span>
                    <span className="font-medium text-fg">{w.name}</span>
                  </TableCell>
                  <TableCell className={`${TD} num text-right`}>
                    {w.ready}/{w.desired}
                  </TableCell>
                  <TableCell className={`${TD} max-w-[22rem]`}>
                    <span className="num block truncate text-xs text-muted" title={images}>
                      {images || "–"}
                    </span>
                  </TableCell>
                  <TableCell className={TD}>
                    {w.protected && (
                      <Badge variant="outline" className="gap-1 text-warn">
                        <Lock aria-hidden="true" />
                        protected
                      </Badge>
                    )}
                  </TableCell>
                  <TableCell className={`${TD} text-right`}>
                    <div className="flex items-center justify-end gap-1">
                      {w.restartable && (
                        <Button
                          variant="ghost"
                          size="xs"
                          aria-label={`Restart ${w.name}`}
                          onClick={() =>
                            ui.openRestart({
                              kind: w.kind,
                              namespace: w.namespace,
                              name: w.name,
                              protected: w.protected,
                            })
                          }
                        >
                          <RotateCw aria-hidden="true" />
                          Restart
                        </Button>
                      )}
                      <HeadlampIcon links={w.links} />
                    </div>
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}
      <ShowAll shown={shown.length} total={rows.length} onAll={() => setAll(true)} />
    </Panel>
  );
}
