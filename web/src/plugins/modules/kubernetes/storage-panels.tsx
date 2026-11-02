import { Archive, HardDrive, Search, Waypoints } from "lucide-react";
import { useMemo } from "react";
import { useTopic } from "@/api/stream";
import type { BackupJob, Health, PVC } from "@/api/types";
import { BackupAge } from "@/components/backup-age";
import { BytesBar } from "@/components/bytes-bar";
import { HeadlampIcon } from "@/components/links";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { HealthBadge } from "@/components/status";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatBytes, formatNumber } from "@/lib/format";
import { HEALTH_TEXT } from "@/lib/health";
import { useQueryParam } from "@/lib/query-param";
import { cn } from "@/lib/utils";

const SORTS = ["worst", "fullest", "days", "name"] as const;
type Sort = (typeof SORTS)[number];
const SORT_LABEL: Record<Sort, string> = {
  worst: "Worst first",
  fullest: "Most full first",
  days: "Soonest full first",
  name: "Name",
};

const HEALTH_RANK: Record<Health, number> = { crit: 0, warn: 1, unknown: 2, ok: 3 };

/** Days until the volume is full at the current growth, when it is growing. */
function daysToFull(p: PVC): number | undefined {
  const growth = p.growthBytesPerDay;
  if (p.usedBytes == null || growth == null || growth <= 0) return undefined;
  return Math.max(0, (p.capacityBytes - p.usedBytes) / growth);
}

function daysHealth(days: number | undefined): Health {
  if (days === undefined) return "unknown";
  if (days < 3) return "crit";
  if (days < 14) return "warn";
  return "ok";
}

function formatGrowth(perDay: number | undefined): string {
  if (perDay == null || !Number.isFinite(perDay)) return "–";
  if (perDay === 0) return "0 B/d";
  return `${perDay > 0 ? "+" : "−"}${formatBytes(Math.abs(perDay))}/d`;
}

function sortPvcs(items: PVC[], sort: Sort): PVC[] {
  const byName = (a: PVC, b: PVC) =>
    a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name);
  const ratio = (p: PVC) => p.usedRatio ?? -1;
  const days = (p: PVC) => daysToFull(p) ?? Number.POSITIVE_INFINITY;
  const out = [...items];
  switch (sort) {
    case "fullest":
      return out.sort((a, b) => ratio(b) - ratio(a) || byName(a, b));
    case "days":
      return out.sort((a, b) => days(a) - days(b) || ratio(b) - ratio(a) || byName(a, b));
    case "name":
      return out.sort(byName);
    default:
      return out.sort(
        (a, b) =>
          HEALTH_RANK[a.health] - HEALTH_RANK[b.health] || ratio(b) - ratio(a) || byName(a, b),
      );
  }
}

function SearchBox({
  value,
  onChange,
  label,
}: {
  value: string;
  onChange: (v: string) => void;
  label: string;
}) {
  return (
    <div className="relative w-40 sm:w-56">
      <Search
        className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted"
        aria-hidden="true"
      />
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="Search volumes"
        aria-label={label}
        className="pl-8"
      />
    </div>
  );
}

function PvcTable({ pvcs }: { pvcs: PVC[] }) {
  const [q, setQ] = useQueryParam("q");
  const [sortParam, setSort] = useQueryParam("sort", "worst");
  const sort: Sort = (SORTS as readonly string[]).includes(sortParam)
    ? (sortParam as Sort)
    : "worst";
  const needle = q.trim().toLowerCase();
  const rows = useMemo(
    () =>
      sortPvcs(
        pvcs.filter(
          (p) =>
            !needle || `${p.namespace}/${p.name} ${p.storageClass}`.toLowerCase().includes(needle),
        ),
        sort,
      ),
    [pvcs, needle, sort],
  );
  return (
    <>
      <div className="flex flex-wrap items-center gap-2 px-4 pb-3">
        <SearchBox value={q} onChange={setQ} label="Search volumes" />
        <Select value={sort} onValueChange={setSort}>
          <SelectTrigger size="sm" aria-label="Sort volumes">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SORTS.map((s) => (
              <SelectItem key={s} value={s}>
                {SORT_LABEL[s]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <span className="num ml-auto text-xs text-muted">
          {rows.length}
          {rows.length !== pvcs.length && ` of ${pvcs.length}`} volumes
        </span>
      </div>
      {rows.length === 0 ? (
        <Empty>{pvcs.length === 0 ? "No persistent volume claims." : "Nothing matches."}</Empty>
      ) : (
        <Table className="min-w-[860px]">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="text-xs text-muted">Volume</TableHead>
              <TableHead className="text-xs text-muted">Class</TableHead>
              <TableHead className="w-[280px] text-xs text-muted">Fill</TableHead>
              <TableHead className="text-right text-xs text-muted">Growth</TableHead>
              <TableHead className="text-right text-xs text-muted">Days to full</TableHead>
              <TableHead className="text-xs text-muted">Health</TableHead>
              <TableHead className="w-10 text-xs text-muted">
                <span className="sr-only">Links</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((p) => (
              <PvcRow key={`${p.namespace}/${p.name}`} pvc={p} />
            ))}
          </TableBody>
        </Table>
      )}
    </>
  );
}

function PvcRow({ pvc: p }: { pvc: PVC }) {
  const days = daysToFull(p);
  const dh = daysHealth(days);
  return (
    <TableRow>
      <TableCell>
        <span className="text-fg">
          <span className="text-muted">{p.namespace}/</span>
          {p.name}
        </span>
        {p.phase !== "Bound" && <span className="ml-2 text-xs text-warn">{p.phase}</span>}
      </TableCell>
      <TableCell className="text-xs text-muted">{p.storageClass || "–"}</TableCell>
      <TableCell>
        <BytesBar
          used={p.usedBytes}
          total={p.capacityBytes}
          label={`${p.namespace}/${p.name}`}
          labelHidden
        />
      </TableCell>
      <TableCell className="num text-right text-xs text-muted">
        {formatGrowth(p.growthBytesPerDay)}
      </TableCell>
      <TableCell
        className={cn(
          "num text-right text-xs",
          dh === "unknown" ? "text-faint" : cn("font-medium", HEALTH_TEXT[dh]),
        )}
      >
        {days === undefined
          ? "–"
          : `${dh === "crit" ? "! " : dh === "warn" ? "⚠ " : ""}${formatNumber(days, days < 10 ? 1 : 0)} d`}
      </TableCell>
      <TableCell>
        <HealthBadge health={p.health} />
      </TableCell>
      <TableCell>
        <HeadlampIcon links={p.links} />
      </TableCell>
    </TableRow>
  );
}

function BackupRow({ b }: { b: BackupJob }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 py-3">
      <span className="truncate text-sm text-fg">{b.name}</span>
      <BackupAge seconds={b.lastSuccessAgeSeconds} />
    </div>
  );
}

/**
 * Each panel reads `kubernetes.storage` itself, as the Proxmox panel reads its own topic, so the
 * Storage slot carries no envelope; the slot only mounts them while the topic is published.
 */
export function PvcPanel() {
  const env = useTopic("kubernetes.storage");
  if (!env) return <PanelSkeleton lines={6} />;
  return (
    <Panel>
      <PanelHeader title="Persistent volumes" icon={<HardDrive />} sources={env.sources} />
      <PvcTable pvcs={env.data.pvcs} />
    </Panel>
  );
}

/** The backup jobs and the etcd database share one cell of the storage grid. */
export function BackupEtcdPanel() {
  const env = useTopic("kubernetes.storage");
  if (!env) return <PanelSkeleton lines={2} />;
  const { data } = env;
  const jobs = data.backupJobs ?? [];
  return (
    <div className="space-y-4">
      <Panel>
        <PanelHeader title="Backup jobs" icon={<Archive />} />
        <div className="divide-y divide-line px-4 pb-2">
          {jobs.length === 0 && <Empty className="py-4">No backup jobs.</Empty>}
          {jobs.map((b) => (
            <BackupRow key={b.ref || b.name} b={b} />
          ))}
        </div>
      </Panel>
      <Panel>
        <PanelHeader title="etcd database" icon={<Waypoints />} />
        <div className="px-4 pb-4">
          <BytesBar
            used={data.etcd.dbBytes}
            total={data.etcd.quotaBytes}
            label="DB size vs quota"
          />
        </div>
      </Panel>
    </div>
  );
}
