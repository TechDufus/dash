import { GitBranch, LoaderCircle, RefreshCw, Search, ShieldAlert, ShieldCheck } from "lucide-react";
import { useMemo, useState } from "react";
import { useMe } from "@/api/queries";
import { useTopic } from "@/api/stream";
import type { FluxKind, FluxObj, FluxSourceRef } from "@/api/types";
import { HeadlampIcon, headlampLink } from "@/components/links";
import { Page, Reveal } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { HealthBadge } from "@/components/status";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { useQueryParam } from "@/lib/query-param";
import { cn } from "@/lib/utils";
import { reconcileFlux } from "@/plugins/modules/flux/actions";
import { useActionAvailable } from "@/plugins/use-action-available";

const TABS = ["kustomizations", "helmreleases", "sources"] as const;
type TabKey = (typeof TABS)[number];

/** `main@sha1:abcdef0123…` -> `main@abcdef0`; other revisions are cut to a readable length. */
function shortRevision(rev: string): string {
  if (!rev) return "–";
  const at = rev.indexOf("@");
  const ref = at >= 0 ? rev.slice(0, at) : rev;
  const rest = at >= 0 ? rev.slice(at + 1) : "";
  const hash = rest.includes(":") ? (rest.split(":")[1] ?? "") : rest;
  if (at < 0) return /^[0-9a-f]{12,}$/i.test(ref) ? ref.slice(0, 7) : ref;
  return hash ? `${ref}@${hash.slice(0, 7)}` : ref;
}

function key(o: FluxObj): string {
  return `${o.kind}/${o.namespace}/${o.name}`;
}

/** Not ready first, then suspended, then healthy; stable by namespace/name inside each rank. */
function rank(o: FluxObj): number {
  if (o.suspended) return 1;
  return o.ready ? 2 : 0;
}

function sortObjs(items: FluxObj[]): FluxObj[] {
  return [...items].sort(
    (a, b) =>
      rank(a) - rank(b) || a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name),
  );
}

function matches(o: FluxObj, q: string): boolean {
  if (!q) return true;
  const hay = `${o.kind} ${o.namespace} ${o.name} ${o.message} ${o.revision}`.toLowerCase();
  return hay.includes(q);
}

function ReconcileButton({
  kind,
  namespace,
  name,
  label,
}: {
  kind: FluxKind;
  namespace: string;
  name: string;
  label?: string;
}) {
  const [busy, setBusy] = useState(false);
  const run = async () => {
    setBusy(true);
    try {
      await reconcileFlux(kind, namespace, name);
    } finally {
      setBusy(false);
    }
  };
  const Icon = busy ? LoaderCircle : RefreshCw;
  const title = `Reconcile ${kind} ${namespace}/${name}`;
  return label ? (
    <Button variant="outline" size="sm" disabled={busy} onClick={run} title={title}>
      <Icon className={cn(busy && "animate-spin")} aria-hidden="true" />
      {label}
    </Button>
  ) : (
    <Button
      variant="ghost"
      size="icon-xs"
      disabled={busy}
      onClick={run}
      title={title}
      aria-label={title}
    >
      <Icon className={cn(busy && "animate-spin")} aria-hidden="true" />
    </Button>
  );
}

function VerifiedShield({ verified }: { verified: boolean | undefined }) {
  if (verified === undefined) {
    return (
      <span className="text-faint" title="No SourceVerified condition">
        –
      </span>
    );
  }
  return verified ? (
    <span className="inline-flex items-center gap-1 text-ok" title="SourceVerified">
      <ShieldCheck className="size-4" aria-hidden="true" />
      <span className="text-xs">Verified</span>
    </span>
  ) : (
    <span className="inline-flex items-center gap-1 text-crit" title="SourceVerified is false">
      <ShieldAlert className="size-4" aria-hidden="true" />
      <span className="text-xs">Unverified</span>
    </span>
  );
}

/** What the header says about one verified source: the sentence and whether it is a good one. */
function verdict(obj: FluxObj | undefined, source: FluxSourceRef): { text: string; ok: boolean } {
  if (!obj)
    return { text: `${source.kind} ${source.namespace}/${source.name} not found`, ok: false };
  if (obj.verified === true) return { text: "Source verified: signature checked", ok: true };
  if (obj.verified === false) {
    return { text: "Source NOT verified: signature check failed", ok: false };
  }
  return {
    text: obj.specVerify
      ? "Source verification missing: no SourceVerified condition"
      : "Source is not signed: spec.verify is not set",
    ok: false,
  };
}

function Banner({
  source,
  obj,
  now,
  canReconcile,
}: {
  source: FluxSourceRef;
  obj: FluxObj | undefined;
  now: number;
  canReconcile: boolean;
}) {
  const { text, ok } = verdict(obj, source);
  return (
    <Panel className="flex flex-col gap-4 p-4 sm:flex-row sm:items-center">
      <div
        className={cn(
          "flex size-14 shrink-0 items-center justify-center rounded-2xl border",
          ok ? "border-ok/30 bg-ok/10 text-ok" : "border-crit/30 bg-crit/10 text-crit",
        )}
      >
        {ok ? (
          <ShieldCheck className="size-8" aria-hidden="true" />
        ) : (
          <ShieldAlert className="size-8" aria-hidden="true" />
        )}
      </div>
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <GitBranch className="size-4 text-accent" aria-hidden="true" />
          <h2 className="text-sm font-medium text-fg">{source.name}</h2>
          <span className="text-xs text-muted">
            {source.kind} · {source.namespace}
          </span>
          {obj &&
            (obj.suspended ? (
              <span className="rounded-full border border-line px-2 py-0.5 text-xs text-muted">
                Suspended{obj.suspendReason ? ` · ${obj.suspendReason}` : ""}
              </span>
            ) : (
              <HealthBadge
                health={obj.ready ? "ok" : "crit"}
                label={obj.ready ? "Ready" : "Not ready"}
              />
            ))}
        </div>
        <p className={cn("text-sm font-medium", ok ? "text-ok" : "text-crit")}>{text}</p>
        <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted">
          <span>
            Revision{" "}
            <span className="num text-fg" title={obj?.revision}>
              {obj ? shortRevision(obj.revision) : "–"}
            </span>
          </span>
          {obj?.lastTransition && (
            <span title={formatDateTime(obj.lastTransition)}>
              changed {formatRelative(obj.lastTransition, now)}
            </span>
          )}
        </p>
      </div>
      {canReconcile && obj && (
        <ReconcileButton
          kind={source.kind}
          namespace={source.namespace}
          name={source.name}
          label="Reconcile"
        />
      )}
    </Panel>
  );
}

function FluxTable({
  items,
  showVerified,
  canReconcile,
  now,
}: {
  items: FluxObj[];
  showVerified: boolean;
  canReconcile: boolean;
  now: number;
}) {
  // While /me is pending the column stays (as it always did); it goes only once the role is known.
  const mePending = useMe().isPending;
  if (items.length === 0) return <Empty>Nothing matches.</Empty>;
  // An empty Actions column is noise: keep it only while a row has a button or link for this user.
  const showActions = mePending || canReconcile || items.some((o) => headlampLink(o.links));
  return (
    <Table className="min-w-[860px]">
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="w-28 text-xs text-muted">Status</TableHead>
          <TableHead className="text-xs text-muted">Object</TableHead>
          <TableHead className="text-xs text-muted">Revision</TableHead>
          {showVerified && <TableHead className="text-xs text-muted">Verified</TableHead>}
          <TableHead className="text-xs text-muted">Message</TableHead>
          <TableHead className="text-xs text-muted">Changed</TableHead>
          {showActions && (
            <TableHead className="w-20 text-right text-xs text-muted">
              <span className="sr-only">Actions</span>
            </TableHead>
          )}
        </TableRow>
      </TableHeader>
      <TableBody>
        {items.map((o) => (
          <TableRow key={key(o)} className={cn(o.suspended && "opacity-55")}>
            <TableCell>
              {o.suspended ? (
                <span
                  className="inline-flex items-center rounded-full border border-line bg-panel px-2 py-0.5 text-xs font-medium text-muted"
                  title={o.suspendReason || undefined}
                >
                  Suspended
                </span>
              ) : (
                <HealthBadge
                  health={o.ready ? "ok" : "crit"}
                  label={o.ready ? "Ready" : "Not ready"}
                />
              )}
            </TableCell>
            <TableCell>
              <div className="flex flex-col">
                <span className="text-fg">
                  <span className="text-muted">{o.namespace}/</span>
                  {o.name}
                </span>
                <span className="text-[11px] text-faint">{o.kind}</span>
              </div>
            </TableCell>
            <TableCell className="num text-xs text-muted" title={o.revision}>
              {shortRevision(o.revision)}
            </TableCell>
            {showVerified && (
              <TableCell>
                <VerifiedShield verified={o.verified} />
              </TableCell>
            )}
            <TableCell>
              <div
                className="max-w-[360px] truncate text-xs text-muted"
                title={o.suspended && o.suspendReason ? o.suspendReason : o.message}
              >
                {o.suspended && o.suspendReason
                  ? `Suspended: ${o.suspendReason}`
                  : o.message || "–"}
              </div>
            </TableCell>
            <TableCell className="text-xs text-muted" title={formatDateTime(o.lastTransition)}>
              {formatRelative(o.lastTransition, now)}
            </TableCell>
            {showActions && (
              <TableCell>
                <div className="flex items-center justify-end gap-0.5">
                  {canReconcile && (
                    <ReconcileButton kind={o.kind} namespace={o.namespace} name={o.name} />
                  )}
                  <HeadlampIcon links={o.links} />
                </div>
              </TableCell>
            )}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function Counter({
  label,
  value,
  className,
}: {
  label: string;
  value: string;
  className?: string;
}) {
  return (
    <span className="inline-flex items-baseline gap-1.5 text-xs text-muted">
      {label}
      <span className={cn("num text-sm font-medium text-fg", className)}>{value}</span>
    </span>
  );
}

export function GitOpsPage() {
  const env = useTopic("flux.objects");
  const canReconcile = useActionAvailable("flux", "reconcile");
  const data = env?.data;
  const now = useNow(30_000).getTime();
  const [q, setQ] = useQueryParam("q");
  const [tabParam, setTab] = useQueryParam("tab", "kustomizations");
  const tab: TabKey = (TABS as readonly string[]).includes(tabParam)
    ? (tabParam as TabKey)
    : "kustomizations";
  const needle = q.trim().toLowerCase();

  const lists = useMemo(() => {
    if (!data) return undefined;
    return {
      kustomizations: sortObjs(data.kustomizations.filter((o) => matches(o, needle))),
      helmreleases: sortObjs(data.helmReleases.filter((o) => matches(o, needle))),
      sources: sortObjs(data.sources.filter((o) => matches(o, needle))),
    } satisfies Record<TabKey, FluxObj[]>;
  }, [data, needle]);

  const verifiedSources = data?.verifiedSources ?? [];
  const active = lists?.[tab] ?? [];
  const ready = active.filter((o) => !o.suspended && o.ready).length;
  const suspended = active.filter((o) => o.suspended).length;
  const notReady = active.length - ready - suspended;

  return (
    <Page>
      {!data ? (
        <Reveal index={0}>
          <PanelSkeleton lines={1} />
        </Reveal>
      ) : (
        verifiedSources.length > 0 && (
          <Reveal index={0}>
            <div className="space-y-3">
              {verifiedSources.map((s) => (
                <Banner
                  key={`${s.kind}/${s.namespace}/${s.name}`}
                  source={s}
                  obj={data.sources.find(
                    (o) => o.kind === s.kind && o.namespace === s.namespace && o.name === s.name,
                  )}
                  now={now}
                  canReconcile={canReconcile}
                />
              ))}
            </div>
          </Reveal>
        )
      )}
      <Reveal index={1}>
        {!data || !lists ? (
          <PanelSkeleton lines={8} />
        ) : (
          <Panel>
            <PanelHeader title="Flux objects" icon={<GitBranch />} sources={env?.sources}>
              <div className="relative w-44 sm:w-64">
                <Search
                  className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted"
                  aria-hidden="true"
                />
                <Input
                  value={q}
                  onChange={(e) => setQ(e.target.value)}
                  placeholder="Filter objects"
                  aria-label="Filter Flux objects"
                  className="pl-8"
                />
              </div>
            </PanelHeader>
            <Tabs value={tab} onValueChange={setTab} className="gap-0">
              <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 border-b border-line px-4 pb-3">
                <TabsList variant="line">
                  <TabsTrigger value="kustomizations">
                    Kustomizations
                    <span className="num text-xs text-muted">{lists.kustomizations.length}</span>
                  </TabsTrigger>
                  <TabsTrigger value="helmreleases">
                    HelmReleases
                    <span className="num text-xs text-muted">{lists.helmreleases.length}</span>
                  </TabsTrigger>
                  <TabsTrigger value="sources">
                    Sources
                    <span className="num text-xs text-muted">{lists.sources.length}</span>
                  </TabsTrigger>
                </TabsList>
                <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
                  <Counter label="Ready" value={`${ready}/${active.length}`} className="text-ok" />
                  <Counter
                    label="Not ready"
                    value={String(notReady)}
                    className={notReady > 0 ? "text-crit" : undefined}
                  />
                  <Counter label="Suspended" value={String(suspended)} />
                </div>
              </div>
              {TABS.map((t) => (
                <TabsContent key={t} value={t}>
                  <FluxTable
                    items={lists[t]}
                    showVerified={t === "sources"}
                    canReconcile={canReconcile}
                    now={now}
                  />
                </TabsContent>
              ))}
            </Tabs>
          </Panel>
        )}
      </Reveal>
    </Page>
  );
}
