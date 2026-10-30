import { ArrowRight, CircleCheck, CircleHelp, CircleX, RotateCw } from "lucide-react";
import { type ReactNode, useEffect, useMemo, useRef, useState } from "react";
import { Link, useLocation, useSearchParams } from "wouter";
import { ApiError, errorMessage } from "@/api/client";
import { usePodDetail } from "@/api/queries";
import { useTopicData } from "@/api/stream";
import type { ClusterData, Condition, Container, Event, OwnerRef, Pod } from "@/api/types";
import { ui } from "@/api/ui-store";
import { LinkButtons } from "@/components/links";
import { Empty } from "@/components/panel";
import { SeriesChart } from "@/components/series-chart";
import { HealthBadge } from "@/components/status";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatBytes, formatDateTime, formatRelative, shortImage } from "@/lib/format";
import { cn } from "@/lib/utils";
import { filterSearch, formatCores } from "./cluster/common";
import { PodLogs } from "./cluster/pod-logs";

const RESTARTABLE = ["Deployment", "StatefulSet", "DaemonSet"] as const;
type RestartableKind = (typeof RESTARTABLE)[number];

function isRestartable(kind: string): kind is RestartableKind {
  return (RESTARTABLE as readonly string[]).includes(kind);
}

type SheetTab = "overview" | "logs" | "metrics" | "events" | "links";

const TABS: { value: SheetTab; label: string }[] = [
  { value: "overview", label: "Overview" },
  { value: "logs", label: "Logs" },
  { value: "metrics", label: "Metrics" },
  { value: "events", label: "Events" },
  { value: "links", label: "Links" },
];

const TH = "h-8 px-2 text-xs font-medium text-muted";

interface PodSheetProps {
  ns: string;
  name: string;
  /** False while the sheet slides out after the route has already changed. */
  open?: boolean;
}

export function PodSheet({ ns, name, open = true }: PodSheetProps) {
  const [, navigate] = useLocation();
  const [params] = useSearchParams();
  const close = () => navigate(backToPods(params));

  return (
    <Sheet open={open} onOpenChange={(o) => !o && close()}>
      <SheetContent className="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[560px]">
        <PodBody key={`${ns}/${name}`} ns={ns} name={name} open={open} />
      </SheetContent>
    </Sheet>
  );
}

function backToPods(params: URLSearchParams): string {
  const next = new URLSearchParams(filterSearch(params));
  next.set("tab", "pods");
  return `/cluster?${next.toString()}`;
}

function PodBody({ ns, name, open }: { ns: string; name: string; open: boolean }) {
  const [params] = useSearchParams();
  const [tab, setTab] = useState<SheetTab>("overview");
  const detail = usePodDetail(ns, name);
  const cluster = useTopicData("cluster");
  const livePod = cluster?.pods.find((p) => p.namespace === ns && p.name === name);
  const pod: Pod | undefined = livePod ?? detail.data?.pod;

  // The detail is fetched on open only; re-fetch when the live pod visibly changes state.
  const stateKey = livePod
    ? `${livePod.phase}|${livePod.reason}|${livePod.readyContainers}|${livePod.restarts}`
    : undefined;
  const seenKey = useRef(stateKey);
  const refetch = detail.refetch;
  useEffect(() => {
    if (stateKey === undefined || seenKey.current === stateKey) return;
    seenKey.current = stateKey;
    void refetch();
  }, [stateKey, refetch]);

  const notFound = detail.error instanceof ApiError && detail.error.status === 404;
  const data = detail.data;

  return (
    <>
      <SheetHeader className="gap-2 border-b border-line p-4 pr-14">
        <SheetTitle className="num truncate text-base font-medium text-fg" title={name}>
          {name}
        </SheetTitle>
        <SheetDescription className="sr-only">
          Pod {name} in namespace {ns}
        </SheetDescription>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-muted">
          {pod ? (
            <>
              <HealthBadge health={pod.health} />
              <span className="text-fg">
                {pod.phase}
                {pod.reason && pod.reason !== pod.phase ? ` · ${pod.reason}` : ""}
              </span>
            </>
          ) : (
            <Skeleton className="h-5 w-24" />
          )}
          <span>
            ns <span className="num text-fg">{ns}</span>
          </span>
          {pod?.node && (
            <span>
              node <span className="num text-fg">{pod.node}</span>
            </span>
          )}
        </div>
      </SheetHeader>

      {notFound ? (
        <div className="space-y-3 p-6 text-center text-sm text-muted">
          <p className="text-fg">Pod not found</p>
          <p>It may have been replaced by a new pod after a restart or rollout.</p>
          <Button asChild variant="outline" size="sm">
            <Link href={backToPods(params)}>Back to Pods</Link>
          </Button>
        </div>
      ) : detail.isError ? (
        <div className="space-y-3 p-6 text-center text-sm">
          <p className="text-crit">{errorMessage(detail.error)}</p>
          <Button type="button" variant="outline" size="sm" onClick={() => void detail.refetch()}>
            Retry
          </Button>
        </div>
      ) : (
        <Tabs
          value={tab}
          onValueChange={(v) => setTab(v as SheetTab)}
          className="min-h-0 flex-1 gap-0"
        >
          <div className="overflow-x-auto border-b border-line px-3 pt-2 pb-3">
            <TabsList variant="line">
              {TABS.map((t) => (
                <TabsTrigger key={t.value} value={t.value} className="flex-none px-3">
                  {t.label}
                </TabsTrigger>
              ))}
            </TabsList>
          </div>

          <TabsContent value="overview" className="min-h-0 flex-1 overflow-y-auto p-4">
            {!data ? (
              <OverviewSkeleton />
            ) : (
              <Overview
                containers={data.containers}
                ownerChain={ownerChainOf(data.ownerChain, pod)}
                ips={data.ips}
                conditions={data.conditions}
                podName={name}
                ns={ns}
                cluster={cluster}
                gone={cluster !== undefined && !livePod}
              />
            )}
          </TabsContent>

          <TabsContent value="logs" className="flex min-h-0 flex-1 flex-col p-4">
            <PodLogs
              ns={ns}
              pod={name}
              containers={data?.containers.map((c) => c.name) ?? []}
              active={open && tab === "logs"}
            />
          </TabsContent>

          <TabsContent value="metrics" className="min-h-0 flex-1 space-y-6 overflow-y-auto p-4">
            <SeriesChart
              name="pod.cpu"
              range="1h"
              params={{ ns, pod: name }}
              format={formatCores}
              title="CPU (cores), last hour"
              height={180}
              enabled={open && tab === "metrics"}
            />
            <SeriesChart
              name="pod.mem"
              range="1h"
              params={{ ns, pod: name }}
              format={formatBytes}
              title="Memory, last hour"
              height={180}
              enabled={open && tab === "metrics"}
            />
          </TabsContent>

          <TabsContent value="events" className="min-h-0 flex-1 overflow-y-auto p-4">
            {!data ? <Skeleton className="h-24 w-full" /> : <PodEvents events={data.events} />}
          </TabsContent>

          <TabsContent value="links" className="min-h-0 flex-1 overflow-y-auto p-4">
            <PodLinks
              pod={pod}
              owner={ownerChainOf(data?.ownerChain, pod).at(-1)}
              cluster={cluster}
            />
          </TabsContent>
        </Tabs>
      )}
    </>
  );
}

/** The owner chain, falling back to the pod's top-level owner when the detail has none. */
function ownerChainOf(chain: OwnerRef[] | undefined, pod: Pod | undefined): OwnerRef[] {
  if (chain && chain.length > 0) return chain;
  return pod?.owner ? [pod.owner] : [];
}

function OverviewSkeleton() {
  return (
    <div className="space-y-4" aria-busy="true">
      <Skeleton className="h-4 w-1/3" />
      <Skeleton className="h-20 w-full" />
      <Skeleton className="h-4 w-1/4" />
      <Skeleton className="h-10 w-full" />
      <Skeleton className="h-4 w-1/4" />
      <Skeleton className="h-16 w-full" />
    </div>
  );
}

function SectionTitle({ children }: { children: ReactNode }) {
  return (
    <h3 className="mb-2 text-xs font-medium tracking-wide text-muted uppercase">{children}</h3>
  );
}

interface OverviewProps {
  containers: Container[];
  ownerChain: OwnerRef[];
  ips: string[];
  conditions: Condition[];
  podName: string;
  ns: string;
  cluster: ClusterData | undefined;
  /** The cluster topic has arrived and no longer lists this pod. */
  gone: boolean;
}

function Overview({
  containers,
  ownerChain,
  ips,
  conditions,
  podName,
  ns,
  cluster,
  gone,
}: OverviewProps) {
  // Restart acts on the top-most restartable workload of the chain.
  const target = useMemo(() => {
    for (let i = ownerChain.length - 1; i >= 0; i--) {
      const o = ownerChain[i];
      if (o && isRestartable(o.kind)) return { kind: o.kind, name: o.name };
    }
    return undefined;
  }, [ownerChain]);

  const restart = () => {
    if (!target) return;
    const workload = cluster?.workloads.find(
      (w) => w.kind === target.kind && w.namespace === ns && w.name === target.name,
    );
    const namespace = cluster?.namespaces.find((n) => n.name === ns);
    // Unknown protection is treated as protected, so the dialog asks for confirmation.
    ui.openRestart({
      kind: target.kind,
      namespace: ns,
      name: target.name,
      protected: workload?.protected ?? namespace?.protected ?? true,
    });
  };

  return (
    <div className="space-y-6">
      {gone && (
        <p className="rounded-lg border border-warn/30 bg-warn/10 px-3 py-2 text-xs text-warn">
          This pod no longer appears in the live cluster state; the details below may be stale.
        </p>
      )}

      <section>
        <SectionTitle>Containers</SectionTitle>
        {containers.length === 0 ? (
          <Empty className="py-4">No containers</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="border-line hover:bg-transparent">
                <TableHead className={TH}>Container</TableHead>
                <TableHead className={TH}>State</TableHead>
                <TableHead className={TH}>Ready</TableHead>
                <TableHead className={`${TH} text-right`}>Restarts</TableHead>
                <TableHead className={TH}>Last exit</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {containers.map((c) => (
                <TableRow key={c.name} className="border-line">
                  <TableCell className="px-2 py-2">
                    <div className="num font-medium text-fg">{c.name}</div>
                    <div className="num max-w-56 truncate text-xs text-muted" title={c.image}>
                      {shortImage(c.image)}
                    </div>
                  </TableCell>
                  <TableCell className="px-2 py-2 text-fg">{c.state}</TableCell>
                  <TableCell className="px-2 py-2">
                    <span
                      className={cn(
                        "inline-flex items-center gap-1 text-xs",
                        c.ready ? "text-ok" : "text-warn",
                      )}
                    >
                      {c.ready ? (
                        <CircleCheck className="size-3.5" aria-hidden="true" />
                      ) : (
                        <CircleX className="size-3.5" aria-hidden="true" />
                      )}
                      {c.ready ? "Ready" : "Not ready"}
                    </span>
                  </TableCell>
                  <TableCell className="num px-2 py-2 text-right">{c.restarts}</TableCell>
                  <TableCell className="px-2 py-2 text-muted">
                    {c.lastTerminationReason || "–"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </section>

      <section>
        <SectionTitle>Owner</SectionTitle>
        <div className="flex flex-wrap items-center gap-2 text-sm">
          {ownerChain.length === 0 ? (
            <span className="text-muted">No owner (bare pod)</span>
          ) : (
            [...ownerChain].reverse().map((o) => (
              <span key={`${o.kind}/${o.name}`} className="inline-flex items-center gap-2">
                <span>
                  <span className="text-muted">{o.kind}</span>{" "}
                  <span className="num text-fg">{o.name}</span>
                </span>
                <ArrowRight className="size-3 text-faint" aria-hidden="true" />
              </span>
            ))
          )}
          {ownerChain.length > 0 && (
            <span className="num text-muted" title={podName}>
              pod
            </span>
          )}
        </div>
        {target && (
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="mt-3"
            disabled={cluster === undefined}
            title={cluster === undefined ? "Waiting for cluster data" : undefined}
            onClick={restart}
          >
            <RotateCw aria-hidden="true" />
            Restart {target.kind} {target.name}
          </Button>
        )}
      </section>

      <section>
        <SectionTitle>IP addresses</SectionTitle>
        {ips.length === 0 ? (
          <span className="text-sm text-muted">None assigned</span>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {ips.map((ip) => (
              <Badge key={ip} variant="outline" className="num">
                {ip}
              </Badge>
            ))}
          </div>
        )}
      </section>

      <section>
        <SectionTitle>Conditions</SectionTitle>
        {conditions.length === 0 ? (
          <span className="text-sm text-muted">No conditions reported</span>
        ) : (
          <ul className="grid gap-1.5 sm:grid-cols-2">
            {conditions.map((c) => (
              <li key={c.type} className="flex items-center gap-2 text-sm">
                <ConditionIcon status={c.status} />
                <span className="text-fg">{c.type}</span>
                <span className="text-xs text-muted">{c.status}</span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function ConditionIcon({ status }: { status: string }) {
  if (status === "True") return <CircleCheck className="size-4 text-ok" aria-hidden="true" />;
  if (status === "False") return <CircleX className="size-4 text-warn" aria-hidden="true" />;
  return <CircleHelp className="size-4 text-faint" aria-hidden="true" />;
}

function PodEvents({ events }: { events: Event[] }) {
  const rows = useMemo(
    () => [...events].sort((a, b) => Date.parse(b.lastSeen) - Date.parse(a.lastSeen)),
    [events],
  );
  if (rows.length === 0) return <Empty>No events for this pod</Empty>;
  return (
    <ul className="space-y-3">
      {rows.map((e) => (
        <li
          key={`${e.reason}/${e.lastSeen}/${e.message}`}
          className="rounded-lg border border-line bg-panel p-3"
        >
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs">
            <span className="font-medium text-warn">{e.reason}</span>
            <span className="num text-muted" title={formatDateTime(e.lastSeen)}>
              {e.count > 1 && <span className="mr-2">×{e.count}</span>}
              {formatRelative(e.lastSeen)}
            </span>
          </div>
          <p className="mt-1 text-sm break-words text-fg">{e.message}</p>
        </li>
      ))}
    </ul>
  );
}

function PodLinks({
  pod,
  owner,
  cluster,
}: {
  pod: Pod | undefined;
  owner: OwnerRef | undefined;
  cluster: ClusterData | undefined;
}) {
  const workload = owner
    ? cluster?.workloads.find(
        (w) => w.kind === owner.kind && w.namespace === pod?.namespace && w.name === owner.name,
      )
    : undefined;
  const hasPod = (pod?.links.length ?? 0) > 0;
  const hasWorkload = (workload?.links.length ?? 0) > 0;
  if (!pod) return <Skeleton className="h-8 w-40" />;
  if (!hasPod && !hasWorkload) return <Empty>No links available</Empty>;
  return (
    <div className="space-y-5">
      {hasPod && (
        <section>
          <SectionTitle>Pod</SectionTitle>
          <LinkButtons links={pod.links} />
        </section>
      )}
      {hasWorkload && workload && (
        <section>
          <SectionTitle>
            {workload.kind} {workload.name}
          </SectionTitle>
          <LinkButtons links={workload.links} />
        </section>
      )}
    </div>
  );
}
