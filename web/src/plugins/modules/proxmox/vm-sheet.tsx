import { Boxes, Camera, Cpu, HardDrive, Network, Server } from "lucide-react";
import { type ReactNode, useState } from "react";
import { errorMessage } from "@/api/client";
import { type SeriesRange, useVMDetail } from "@/api/queries";
import { useTopic, useTopicData } from "@/api/stream";
import type { Link, VM, VMDetail } from "@/api/types";
import { LinkButtons } from "@/components/links";
import { Empty, Panel } from "@/components/panel";
import { SeriesChart } from "@/components/series-chart";
import { HealthBadge } from "@/components/status";
import { Button } from "@/components/ui/button";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime, formatPercent, formatRate } from "@/lib/format";
import { cn } from "@/lib/utils";

const RANGES: { value: SeriesRange; label: string }[] = [
  { value: "1h", label: "1h" },
  { value: "6h", label: "6h" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
];

type SheetTab = "overview" | "metrics" | "links";

function Section({
  title,
  icon,
  children,
}: {
  title: string;
  icon?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="space-y-2">
      <h3 className="flex items-center gap-1.5 text-xs font-medium tracking-wide text-muted uppercase [&_svg]:size-3.5">
        {icon}
        {title}
      </h3>
      {children}
    </section>
  );
}

function ErrorNote({ error }: { error: unknown }) {
  return (
    <Panel role="alert" className="border-crit/30 bg-crit/10 p-3 text-sm text-crit">
      Could not load VM details: {errorMessage(error)}
    </Panel>
  );
}

function SpecList({ items, empty }: { items: { key: string; spec: string }[]; empty: string }) {
  if (items.length === 0) return <p className="text-sm text-muted">{empty}</p>;
  return (
    <ul className="space-y-1.5">
      {items.map((it) => (
        <li
          key={it.key}
          className="flex flex-col gap-0.5 rounded-lg border border-line bg-panel px-3 py-2 sm:flex-row sm:gap-3"
        >
          <span className="num shrink-0 text-fg">{it.key}</span>
          <span className="num min-w-0 text-xs break-all text-muted">{it.spec}</span>
        </li>
      ))}
    </ul>
  );
}

function ConfigRow({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="flex flex-col gap-0.5">
      <dt className="text-xs text-muted">{label}</dt>
      <dd className="num text-fg">{value}</dd>
    </div>
  );
}

function OverviewTab({
  vm,
  detail,
  isPending,
  error,
}: {
  vm: VM | undefined;
  detail: VMDetail | undefined;
  isPending: boolean;
  error: unknown;
}) {
  const node = vm?.k8sNode;
  const headlamp = vm?.links.filter((l) => l.kind === "headlamp") ?? [];
  const cfg = detail?.config;
  return (
    <div className="space-y-5">
      {error != null && !detail && <ErrorNote error={error} />}
      {isPending && (
        <div className="space-y-3" aria-busy="true">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-16 w-full" />
          <Skeleton className="h-16 w-full" />
        </div>
      )}
      {cfg && (
        <>
          <Section title="Configuration" icon={<Cpu aria-hidden="true" />}>
            <Panel className="p-3">
              <dl className="grid grid-cols-2 gap-x-4 gap-y-3 text-sm">
                <ConfigRow
                  label="CPU"
                  value={`${cfg.cores} × ${cfg.sockets} (${cfg.cores * cfg.sockets} vCPU)`}
                />
                <ConfigRow label="Memory" value={`${cfg.memoryMiB} MiB`} />
                <ConfigRow
                  label="Balloon"
                  value={cfg.balloonMiB > 0 ? `${cfg.balloonMiB} MiB` : "disabled"}
                />
                <ConfigRow label="OS type" value={cfg.ostype || "–"} />
                <ConfigRow label="Guest agent" value={cfg.agent || "–"} />
              </dl>
            </Panel>
          </Section>
          <Section title="Disks" icon={<HardDrive aria-hidden="true" />}>
            <SpecList items={cfg.disks} empty="No disks configured." />
          </Section>
          <Section title="Network interfaces" icon={<Network aria-hidden="true" />}>
            <SpecList items={cfg.nics} empty="No network interfaces configured." />
          </Section>
        </>
      )}
      {detail && (
        <>
          <Section title="IP addresses" icon={<Network aria-hidden="true" />}>
            {detail.ips.length === 0 ? (
              <p className="text-sm text-muted">No guest agent data, so no addresses to show.</p>
            ) : (
              <ul className="space-y-1.5">
                {detail.ips.map((ip) => (
                  <li
                    key={`${ip.iface}:${ip.address}`}
                    className="flex items-baseline justify-between gap-3 rounded-lg border border-line bg-panel px-3 py-2"
                  >
                    <span className="num text-xs text-muted">{ip.iface}</span>
                    <span className="num text-fg">{ip.address}</span>
                  </li>
                ))}
              </ul>
            )}
          </Section>
          <Section title="Snapshots" icon={<Camera aria-hidden="true" />}>
            {detail.snapshots.length === 0 ? (
              <p className="text-sm text-muted">No snapshots.</p>
            ) : (
              <ul className="space-y-1.5">
                {detail.snapshots.map((s) => (
                  <li key={s.name} className="rounded-lg border border-line bg-panel px-3 py-2">
                    <div className="flex items-baseline justify-between gap-3">
                      <span className="num text-fg">{s.name}</span>
                      <span className="num shrink-0 text-xs text-muted">
                        {formatDateTime(s.time)}
                      </span>
                    </div>
                    {s.description && (
                      <p className="mt-0.5 text-xs break-words text-muted">{s.description}</p>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </Section>
        </>
      )}
      {node && (
        <Section title="Kubernetes node" icon={<Boxes aria-hidden="true" />}>
          <Panel className="flex flex-wrap items-center gap-x-3 gap-y-2 p-3">
            <span className="num text-fg">{node.name}</span>
            <HealthBadge
              health={node.ready ? "ok" : "crit"}
              label={node.ready ? "Ready" : "NotReady"}
            />
            <LinkButtons links={headlamp} className="ml-auto" />
          </Panel>
        </Section>
      )}
    </div>
  );
}

function RangeSelector({
  value,
  onChange,
}: {
  value: SeriesRange;
  onChange: (r: SeriesRange) => void;
}) {
  return (
    <fieldset className="m-0 inline-flex gap-1 rounded-lg border border-line bg-panel p-0.5">
      <legend className="sr-only">Time range</legend>
      {RANGES.map((r) => (
        <Button
          key={r.value}
          size="xs"
          variant={r.value === value ? "secondary" : "ghost"}
          aria-pressed={r.value === value}
          onClick={() => onChange(r.value)}
          className={cn("num", r.value === value ? "text-accent" : "text-muted")}
        >
          {r.label}
        </Button>
      ))}
    </fieldset>
  );
}

function MetricsTab({ vmid, active }: { vmid: string; active: boolean }) {
  const [range, setRange] = useState<SeriesRange>("1h");
  const params = { vmid };
  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-3">
        <span className="text-xs text-muted">Range</span>
        <RangeSelector value={range} onChange={setRange} />
      </div>
      <Panel className="p-3">
        <SeriesChart
          title="CPU"
          name="vm.cpu"
          range={range}
          params={params}
          format={formatPercent}
          domain={[0, 1]}
          enabled={active}
        />
      </Panel>
      <Panel className="p-3">
        <SeriesChart
          title="Memory (used of allocated)"
          name="vm.mem"
          range={range}
          params={params}
          format={formatPercent}
          domain={[0, 1]}
          enabled={active}
        />
      </Panel>
      <Panel className="p-3">
        <SeriesChart
          title="Network in"
          name="vm.netIn"
          range={range}
          params={params}
          format={formatRate}
          enabled={active}
        />
      </Panel>
      <Panel className="p-3">
        <SeriesChart
          title="Network out"
          name="vm.netOut"
          range={range}
          params={params}
          format={formatRate}
          enabled={active}
        />
      </Panel>
      <Panel className="p-3">
        <SeriesChart
          title="Disk read"
          name="vm.diskRead"
          range={range}
          params={params}
          format={formatRate}
          enabled={active}
        />
      </Panel>
      <Panel className="p-3">
        <SeriesChart
          title="Disk write"
          name="vm.diskWrite"
          range={range}
          params={params}
          format={formatRate}
          enabled={active}
        />
      </Panel>
    </div>
  );
}

function LinksTab({ vm, hostLinks }: { vm: VM | undefined; hostLinks: Link[] }) {
  const own = vm?.links ?? [];
  const seen = new Set(own.map((l) => l.href));
  const host = hostLinks.filter((l) => !seen.has(l.href));
  if (own.length === 0 && host.length === 0) return <Empty>No links for this VM.</Empty>;
  return (
    <div className="space-y-5">
      {own.length > 0 && (
        <Section title="This VM" icon={<Server aria-hidden="true" />}>
          <LinkButtons links={own} size="sm" />
        </Section>
      )}
      {host.length > 0 && (
        <Section title="Host" icon={<Server aria-hidden="true" />}>
          <LinkButtons links={host} size="sm" />
        </Section>
      )}
    </div>
  );
}

function VMSheetBody({ vmid }: { vmid: string }) {
  const [tab, setTab] = useState<SheetTab>("overview");
  const topic = useTopic("proxmox.vms");
  const host = useTopicData("host");
  const detail = useVMDetail(vmid);
  const live = topic?.data.vms.find((v) => String(v.vmid) === vmid);
  const vm = live ?? detail.data?.vm;

  if (topic && !live) {
    return (
      <>
        <SheetHeader className="border-b border-line pr-12">
          <SheetTitle>VM not found</SheetTitle>
          <SheetDescription className="num">VMID {vmid}</SheetDescription>
        </SheetHeader>
        <Empty>VM not found. No VM with ID {vmid} is known to Proxmox.</Empty>
      </>
    );
  }

  return (
    <>
      <SheetHeader className="gap-1.5 border-b border-line pr-12">
        {vm ? (
          <>
            <SheetTitle className="flex flex-wrap items-baseline gap-x-2">
              <span>{vm.name}</span>
              <span className="num text-sm font-normal text-muted">#{vm.vmid}</span>
            </SheetTitle>
            <SheetDescription asChild>
              <div className="flex flex-wrap items-center gap-2">
                <HealthBadge health={vm.health} label={vm.status} />
                {vm.template && <span className="text-xs text-muted">template</span>}
              </div>
            </SheetDescription>
          </>
        ) : (
          <>
            <SheetTitle className="num">VM {vmid}</SheetTitle>
            <SheetDescription className="sr-only">Loading VM details</SheetDescription>
            <Skeleton className="h-5 w-24" />
          </>
        )}
      </SheetHeader>
      <Tabs
        value={tab}
        onValueChange={(v) => setTab(v as SheetTab)}
        className="min-h-0 flex-1 gap-0"
      >
        <TabsList variant="line" className="w-full justify-start border-b border-line px-4 pb-1">
          <TabsTrigger value="overview" className="flex-none px-3">
            Overview
          </TabsTrigger>
          <TabsTrigger value="metrics" className="flex-none px-3">
            Metrics
          </TabsTrigger>
          <TabsTrigger value="links" className="flex-none px-3">
            Links
          </TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="min-h-0 overflow-y-auto p-4">
          <OverviewTab
            vm={vm}
            detail={detail.data}
            isPending={detail.isPending}
            error={detail.error}
          />
        </TabsContent>
        <TabsContent value="metrics" className="min-h-0 overflow-y-auto p-4">
          <MetricsTab vmid={vmid} active={tab === "metrics"} />
        </TabsContent>
        <TabsContent value="links" className="min-h-0 overflow-y-auto p-4">
          <LinksTab vm={vm} hostLinks={host?.links ?? []} />
        </TabsContent>
      </Tabs>
    </>
  );
}

/** Routed VM detail drawer: open while `vmid` is set; closing calls `onClose` (navigates to /vms). */
export function VMSheet({ vmid, onClose }: { vmid: string | undefined; onClose: () => void }) {
  // Keep the last id so the content survives the close animation.
  const [shown, setShown] = useState(vmid);
  if (vmid !== undefined && vmid !== shown) setShown(vmid);
  return (
    <Sheet
      open={vmid !== undefined}
      onOpenChange={(o) => {
        if (!o) onClose();
      }}
    >
      <SheetContent className="w-full gap-0 p-0 data-[side=right]:w-full data-[side=right]:sm:max-w-[560px]">
        {shown !== undefined && <VMSheetBody key={shown} vmid={shown} />}
      </SheetContent>
    </Sheet>
  );
}
