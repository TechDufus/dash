import {
  ArrowDown,
  ArrowUp,
  Boxes,
  CircleCheck,
  CircleX,
  Clock,
  Loader,
  Lock,
  MonitorCog,
  TriangleAlert,
} from "lucide-react";
import { useMemo } from "react";
import { Link, useLocation } from "wouter";
import { useTopic } from "@/api/stream";
import type { PVETask, VM } from "@/api/types";
import { BytesBar, PVE_MEM_TIP } from "@/components/bytes-bar";
import { Gauge } from "@/components/gauge";
import { Page, Reveal } from "@/components/page";
import { Empty, Panel, PanelHeader, SourceChips } from "@/components/panel";
import { HealthBadge } from "@/components/status";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatBytes, formatDuration, formatNumber, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { VMSheet } from "./vm-sheet";

const SKELETON_KEYS = ["a", "b", "c", "d", "e", "f"];
const TASK_LIMIT = 20;

function VMCard({ vm }: { vm: VM }) {
  const running = vm.status === "running";
  return (
    <Link
      href={`/vms/${vm.vmid}`}
      aria-label={`${vm.name}, VM ${vm.vmid}, ${vm.status}`}
      className="block h-full rounded-2xl"
    >
      <Panel interactive className="flex h-full flex-col gap-3 p-4">
        <div className="flex items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="truncate text-sm font-medium text-fg">{vm.name}</div>
            <div className="num text-xs text-muted">#{vm.vmid}</div>
          </div>
          <HealthBadge health={vm.health} label={vm.status} />
        </div>

        {(vm.tags.length > 0 || vm.lock || vm.k8sNode) && (
          <div className="flex flex-wrap items-center gap-1.5">
            {vm.lock && (
              <span
                className="inline-flex items-center gap-1 rounded-full border border-warn/30 bg-warn/10 px-2 py-0.5 text-[11px] font-medium text-warn"
                title={`Locked: ${vm.lock}`}
              >
                <Lock className="size-3" aria-hidden="true" />
                locked: {vm.lock}
              </span>
            )}
            {vm.k8sNode && (
              <span
                className={cn(
                  "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium",
                  vm.k8sNode.ready
                    ? "border-ok/25 bg-ok/10 text-ok"
                    : "border-crit/30 bg-crit/10 text-crit",
                )}
              >
                <Boxes className="size-3" aria-hidden="true" />
                {vm.k8sNode.name} · {vm.k8sNode.ready ? "Ready" : "NotReady"}
              </span>
            )}
            {vm.tags.map((t) => (
              <span
                key={t}
                className="rounded-full border border-line bg-panel px-2 py-0.5 text-[11px] text-muted"
              >
                {t}
              </span>
            ))}
          </div>
        )}

        <div className="flex items-center gap-4">
          <div className="flex shrink-0 flex-col items-center gap-0.5">
            <Gauge value={vm.cpuRatio} size={76} strokeWidth={7} caption="CPU" />
            <span className="num text-[11px] text-muted">
              {formatNumber(vm.cpus, vm.cpus % 1 === 0 ? 0 : 1)} vCPU
            </span>
          </div>
          <div className="min-w-0 flex-1 space-y-3">
            <BytesBar
              used={vm.memBytes}
              total={vm.maxMemBytes}
              label="Memory"
              neutral
              labelTip={PVE_MEM_TIP}
            />
            <div className="flex items-center gap-1.5 text-xs text-muted">
              <Clock className="size-3.5" aria-hidden="true" />
              <span>Uptime</span>
              <span className="num ml-auto text-fg">
                {running ? formatDuration(vm.uptimeSeconds) : "–"}
              </span>
            </div>
          </div>
        </div>

        <div className="mt-auto flex items-center justify-between gap-2 border-t border-line pt-2.5 text-xs text-muted">
          <span className="inline-flex items-center gap-1" title="Network in (total)">
            <ArrowDown className="size-3.5" aria-hidden="true" />
            <span className="sr-only">Network in</span>
            <span className="num text-fg">{formatBytes(vm.netInBytes)}</span>
          </span>
          <span className="inline-flex items-center gap-1" title="Network out (total)">
            <ArrowUp className="size-3.5" aria-hidden="true" />
            <span className="sr-only">Network out</span>
            <span className="num text-fg">{formatBytes(vm.netOutBytes)}</span>
          </span>
        </div>
      </Panel>
    </Link>
  );
}

function CardSkeletons() {
  return (
    <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-4" aria-busy="true">
      {SKELETON_KEYS.map((k) => (
        <Panel key={k} className="space-y-3 p-4">
          <div className="flex justify-between gap-3">
            <Skeleton className="h-8 w-1/2" />
            <Skeleton className="h-5 w-16" />
          </div>
          <div className="flex items-center gap-4">
            <Skeleton className="size-[76px] rounded-full" />
            <div className="flex-1 space-y-3">
              <Skeleton className="h-6 w-full" />
              <Skeleton className="h-4 w-full" />
            </div>
          </div>
          <Skeleton className="h-4 w-full" />
        </Panel>
      ))}
    </div>
  );
}

function Templates({ templates }: { templates: VM[] }) {
  if (templates.length === 0) return null;
  return (
    <details className="group rounded-2xl border border-line bg-panel">
      <summary className="flex cursor-pointer list-none items-center gap-2 rounded-2xl px-4 py-3 text-sm font-medium text-fg [&::-webkit-details-marker]:hidden">
        <MonitorCog className="size-4 text-accent" aria-hidden="true" />
        Templates
        <span className="num text-xs text-muted">{templates.length}</span>
      </summary>
      <ul className="grid grid-cols-[repeat(auto-fill,minmax(220px,1fr))] gap-2 px-4 pb-4">
        {templates.map((t) => (
          <li key={t.vmid}>
            <Link
              href={`/vms/${t.vmid}`}
              className="flex items-baseline justify-between gap-2 rounded-lg border border-line bg-panel px-3 py-2 text-sm transition-colors hover:border-accent/30 hover:bg-panel-hover"
            >
              <span className="truncate text-fg">{t.name}</span>
              <span className="num text-xs text-muted">#{t.vmid}</span>
            </Link>
          </li>
        ))}
      </ul>
    </details>
  );
}

type TaskState = "running" | "ok" | "warn" | "failed";

function taskState(t: PVETask): TaskState {
  const s = t.status.trim();
  if (s === "" || s.toLowerCase() === "running") return "running";
  if (s === "OK") return "ok";
  if (s.toUpperCase().startsWith("WARNINGS")) return "warn";
  return "failed";
}

function TaskStatus({ task }: { task: PVETask }) {
  const state = taskState(task);
  if (state === "running") {
    return (
      <span className="inline-flex items-center gap-1 text-accent">
        <Loader className="size-3.5 motion-safe:animate-spin" aria-hidden="true" />
        Running
      </span>
    );
  }
  if (state === "ok") {
    return (
      <span className="inline-flex items-center gap-1 text-ok">
        <CircleCheck className="size-3.5" aria-hidden="true" />
        OK
      </span>
    );
  }
  if (state === "warn") {
    return (
      <span className="inline-flex items-center gap-1 text-warn" title={task.status}>
        <TriangleAlert className="size-3.5" aria-hidden="true" />
        {task.status}
      </span>
    );
  }
  return (
    <span className="inline-flex max-w-[28ch] items-center gap-1 text-crit" title={task.status}>
      <CircleX className="size-3.5 shrink-0" aria-hidden="true" />
      <span className="truncate">Failed: {task.status}</span>
    </span>
  );
}

function taskDuration(t: PVETask): string {
  if (!t.endTime) return "–";
  const secs = (Date.parse(t.endTime) - Date.parse(t.startTime)) / 1000;
  return Number.isFinite(secs) ? formatDuration(secs) : "–";
}

function Activity({ tasks, loaded }: { tasks: PVETask[]; loaded: boolean }) {
  const now = useNow(30_000).getTime();
  const recent = useMemo(
    () =>
      [...tasks]
        .sort((a, b) => Date.parse(b.startTime) - Date.parse(a.startTime))
        .slice(0, TASK_LIMIT),
    [tasks],
  );
  return (
    <Panel>
      <PanelHeader title="Proxmox activity" icon={<Clock />}>
        {loaded && <span className="num text-xs text-muted">last {recent.length}</span>}
      </PanelHeader>
      {!loaded ? (
        <div className="space-y-2 px-4 pb-4" aria-busy="true">
          <Skeleton className="h-8 w-full" />
          <Skeleton className="h-8 w-full" />
          <Skeleton className="h-8 w-full" />
        </div>
      ) : recent.length === 0 ? (
        <Empty className="pt-2">No recent Proxmox tasks.</Empty>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="pl-4">Type</TableHead>
              <TableHead>ID</TableHead>
              <TableHead>User</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Started</TableHead>
              <TableHead className="pr-4 text-right">Duration</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {recent.map((t) => (
              <TableRow key={t.upid}>
                <TableCell className="num pl-4 text-fg">{t.type}</TableCell>
                <TableCell className="num text-muted">{t.id || "–"}</TableCell>
                <TableCell className="num text-muted">{t.user}</TableCell>
                <TableCell>
                  <TaskStatus task={t} />
                </TableCell>
                <TableCell className="num whitespace-nowrap text-muted">
                  {formatRelative(t.startTime, now)}
                </TableCell>
                <TableCell className="num pr-4 text-right text-muted">{taskDuration(t)}</TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Panel>
  );
}

export function VMsPage({ vmid }: { vmid?: string }) {
  const [, navigate] = useLocation();
  const env = useTopic("proxmox.vms");
  const data = env?.data;
  const { vms, templates } = useMemo(() => {
    const sorted = [...(data?.vms ?? [])].sort((a, b) => a.vmid - b.vmid);
    return {
      vms: sorted.filter((v) => !v.template),
      templates: sorted.filter((v) => v.template),
    };
  }, [data?.vms]);
  const running = vms.filter((v) => v.status === "running").length;

  return (
    <Page>
      <Reveal index={0} className="flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
        <span className="text-muted">
          {data ? (
            <>
              <span className="num text-fg">{running}</span> running of{" "}
              <span className="num text-fg">{vms.length}</span> VMs
            </>
          ) : (
            "Loading VMs"
          )}
        </span>
        <SourceChips sources={env?.sources} />
      </Reveal>

      {!data ? (
        <CardSkeletons />
      ) : vms.length === 0 ? (
        <Panel>
          <Empty>No VMs reported by Proxmox.</Empty>
        </Panel>
      ) : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(280px,1fr))] gap-4">
          {vms.map((vm, i) => (
            <Reveal key={vm.vmid} index={i + 1} className="h-full">
              <VMCard vm={vm} />
            </Reveal>
          ))}
        </div>
      )}

      {data && <Templates templates={templates} />}

      <Reveal index={3}>
        <Activity tasks={data?.tasks ?? []} loaded={!!data} />
      </Reveal>

      <VMSheet vmid={vmid} onClose={() => navigate("/vms")} />
    </Page>
  );
}
