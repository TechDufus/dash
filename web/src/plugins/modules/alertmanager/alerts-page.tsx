import {
  BellOff,
  ChevronDown,
  CircleCheck,
  Info,
  OctagonAlert,
  ShieldOff,
  TriangleAlert,
} from "lucide-react";
import { AnimatePresence, motion } from "motion/react";
import { type ReactNode, useMemo, useState } from "react";
import { useTopic } from "@/api/stream";
import type { Alert, Matcher, Silence } from "@/api/types";
import { ui } from "@/api/ui-store";
import { CopyContextButton } from "@/components/copy-context";
import { Page, Reveal } from "@/components/page";
import { Empty, Panel, PanelSkeleton, SourceChips } from "@/components/panel";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { useQueryParam } from "@/lib/query-param";
import { cn } from "@/lib/utils";
import { useActionAvailable } from "@/plugins/use-action-available";

type Level = "critical" | "warning" | "other";

const LEVELS: { key: Level; title: string }[] = [
  { key: "critical", title: "Critical" },
  { key: "warning", title: "Warning" },
  { key: "other", title: "Other" },
];

function levelOf(a: Alert): Level {
  const s = a.severity.toLowerCase();
  if (s === "critical") return "critical";
  if (s === "warning") return "warning";
  return "other";
}

/** The plugin's firing rule, seen from the other side: not active, or silenced or inhibited. */
function isSuppressed(a: Alert): boolean {
  return a.state === "suppressed" || a.silencedBy.length > 0 || a.inhibitedBy.length > 0;
}

function SeverityBadge({ level, label }: { level: Level; label: string }) {
  const Icon = level === "critical" ? OctagonAlert : level === "warning" ? TriangleAlert : Info;
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium",
        level === "critical" && "border-crit/30 bg-crit/10 text-crit",
        level === "warning" && "border-warn/30 bg-warn/10 text-warn",
        level === "other" && "border-line bg-panel text-muted",
      )}
    >
      <Icon className="size-3" aria-hidden="true" />
      {label}
    </span>
  );
}

function Chip({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span
      title={title}
      className="num max-w-full truncate rounded-md border border-line bg-panel px-1.5 py-0.5 text-[11px] text-muted"
    >
      {children}
    </span>
  );
}

function LabelChips({ labels }: { labels: Record<string, string> }) {
  const entries = Object.entries(labels)
    .filter(([k]) => k !== "alertname" && k !== "severity")
    .sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-1.5">
      {entries.map(([k, v]) => (
        <Chip key={k} title={`${k}=${v}`}>
          {k}={v}
        </Chip>
      ))}
    </div>
  );
}

const ITEM_MOTION = {
  layout: "position" as const,
  initial: { opacity: 0, y: 8 },
  animate: { opacity: 1, y: 0 },
  exit: { opacity: 0, scale: 0.97 },
  transition: { duration: 0.22, ease: "easeOut" as const },
};

function AlertCard({
  alert,
  level,
  now,
  canSilence,
}: {
  alert: Alert;
  level: Level;
  now: number;
  canSilence: boolean;
}) {
  return (
    <motion.div {...ITEM_MOTION}>
      <Panel
        className={cn(
          "space-y-3 p-4",
          level === "critical" && "border-crit/25",
          level === "warning" && "border-warn/25",
        )}
      >
        <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
          <div className="min-w-0 flex-1 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <SeverityBadge level={level} label={alert.severity || "unknown"} />
              <h3 className="text-sm font-medium break-all text-fg">{alert.name}</h3>
              {alert.state === "unprocessed" && <Chip>unprocessed</Chip>}
            </div>
            <p className="text-xs text-muted" title={formatDateTime(alert.startsAt)}>
              firing since {formatRelative(alert.startsAt, now)}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <CopyContextButton contextRef={alert.ref} name={alert.name} />
            {canSilence && !alert.protected && (
              <Button variant="outline" size="sm" onClick={() => ui.openSilence(alert)}>
                <BellOff aria-hidden="true" />
                Silence
              </Button>
            )}
          </div>
        </div>
        {alert.summary && <p className="text-sm text-fg">{alert.summary}</p>}
        {alert.description && (
          <details className="group text-sm text-muted">
            <summary className="inline-flex cursor-pointer list-none items-center gap-1 text-xs text-accent outline-none focus-visible:underline [&::-webkit-details-marker]:hidden">
              <ChevronDown
                className="size-3.5 transition-transform group-open:rotate-180"
                aria-hidden="true"
              />
              Details
            </summary>
            <p className="mt-2 whitespace-pre-wrap">{alert.description}</p>
          </details>
        )}
        <LabelChips labels={alert.labels} />
      </Panel>
    </motion.div>
  );
}

function FiringList({
  alerts,
  now,
  canSilence,
}: {
  alerts: Alert[];
  now: number;
  canSilence: boolean;
}) {
  if (alerts.length === 0) {
    return (
      <Panel className="flex flex-col items-center gap-3 px-4 py-12 text-center">
        <span className="flex size-14 items-center justify-center rounded-full border border-ok/30 bg-ok/10 text-ok">
          <CircleCheck className="size-8" aria-hidden="true" />
        </span>
        <p className="text-base font-medium text-fg">No alerts firing</p>
        <p className="text-xs text-muted">Alertmanager has nothing active right now.</p>
      </Panel>
    );
  }
  const groups = LEVELS.map((l) => ({
    ...l,
    items: alerts
      .filter((a) => levelOf(a) === l.key)
      .sort((a, b) => Date.parse(b.startsAt) - Date.parse(a.startsAt)),
  })).filter((g) => g.items.length > 0);
  return (
    <div className="space-y-6">
      {groups.map((g) => (
        <section key={g.key} className="space-y-3">
          <h2 className="flex items-center gap-2 text-xs font-medium tracking-wide text-muted uppercase">
            {g.title}
            <span className="num rounded-full border border-line px-1.5 text-[11px] text-fg">
              {g.items.length}
            </span>
          </h2>
          <div className="grid gap-3 xl:grid-cols-2">
            <AnimatePresence initial={false}>
              {g.items.map((a) => (
                <AlertCard
                  key={a.fingerprint}
                  alert={a}
                  level={g.key}
                  now={now}
                  canSilence={canSilence}
                />
              ))}
            </AnimatePresence>
          </div>
        </section>
      ))}
    </div>
  );
}

function SuppressedRow({
  alert,
  alerts,
  silences,
  now,
}: {
  alert: Alert;
  alerts: Alert[];
  silences: Silence[];
  now: number;
}) {
  const level = levelOf(alert);
  return (
    <motion.div {...ITEM_MOTION} className="space-y-2 py-3">
      <div className="flex flex-wrap items-center gap-2">
        <SeverityBadge level={level} label={alert.severity || "unknown"} />
        <span className="text-sm font-medium break-all text-fg">{alert.name}</span>
        <span className="ml-auto text-xs text-muted" title={formatDateTime(alert.startsAt)}>
          since {formatRelative(alert.startsAt, now)}
        </span>
        <CopyContextButton contextRef={alert.ref} name={alert.name} size="xs" />
      </div>
      {alert.summary && <p className="text-xs text-muted">{alert.summary}</p>}
      <div className="flex flex-wrap items-center gap-1.5 text-xs text-muted">
        {alert.silencedBy.map((id) => {
          const s = silences.find((x) => x.id === id);
          return (
            <Chip key={id} title={`Silence ${id}`}>
              <ShieldOff className="mr-1 inline size-3" aria-hidden="true" />
              silenced by {s ? `${s.createdBy}: ${s.comment}` : id.slice(0, 8)}
            </Chip>
          );
        })}
        {alert.inhibitedBy.map((fp) => {
          const by = alerts.find((x) => x.fingerprint === fp);
          return (
            <Chip key={fp} title={`Inhibiting alert ${fp}`}>
              inhibited by {by ? by.name : fp.slice(0, 8)}
            </Chip>
          );
        })}
      </div>
    </motion.div>
  );
}

function Suppressed({
  alerts,
  all,
  silences,
  now,
}: {
  alerts: Alert[];
  all: Alert[];
  silences: Silence[];
  now: number;
}) {
  const [open, setOpen] = useState(false);
  if (alerts.length === 0) return null;
  return (
    <Panel>
      <button
        type="button"
        aria-expanded={open}
        aria-controls="suppressed-alerts"
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center gap-2 rounded-2xl px-4 py-3 text-left outline-none"
      >
        <ChevronDown
          className={cn("size-4 text-muted transition-transform", open && "rotate-180")}
          aria-hidden="true"
        />
        <span className="text-sm font-medium text-fg">Suppressed</span>
        <span className="num rounded-full border border-line px-1.5 text-[11px] text-fg">
          {alerts.length}
        </span>
        <span className="text-xs text-muted">silenced or inhibited</span>
      </button>
      {open && (
        <div id="suppressed-alerts" className="divide-y divide-line px-4 pb-2">
          <AnimatePresence initial={false}>
            {alerts.map((a) => (
              <SuppressedRow
                key={a.fingerprint}
                alert={a}
                alerts={all}
                silences={silences}
                now={now}
              />
            ))}
          </AnimatePresence>
        </div>
      )}
    </Panel>
  );
}

function matcherOp(m: Matcher): string {
  if (m.isRegex) return m.isEqual ? "=~" : "!~";
  return m.isEqual ? "=" : "!=";
}

function SilenceCard({
  silence: s,
  now,
  canExpire,
}: {
  silence: Silence;
  now: number;
  canExpire: boolean;
}) {
  const pending = s.state === "pending";
  return (
    <motion.div {...ITEM_MOTION}>
      <Panel className="space-y-3 p-4">
        <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
          <div className="min-w-0 flex-1 space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <span
                className={cn(
                  "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium",
                  pending ? "border-warn/30 bg-warn/10 text-warn" : "border-ok/25 bg-ok/10 text-ok",
                )}
              >
                {pending ? (
                  <TriangleAlert className="size-3" aria-hidden="true" />
                ) : (
                  <ShieldOff className="size-3" aria-hidden="true" />
                )}
                {pending ? "Pending" : "Active"}
              </span>
              <span
                className="text-xs text-muted"
                title={`${formatDateTime(s.startsAt)} → ${formatDateTime(s.endsAt)}`}
              >
                {pending
                  ? `starts ${formatRelative(s.startsAt, now)}`
                  : `expires ${formatRelative(s.endsAt, now)}`}
              </span>
            </div>
            <div className="flex flex-wrap gap-1.5">
              {s.matchers.map((m) => (
                <Chip key={`${m.name}${matcherOp(m)}${m.value}`}>
                  {m.name}
                  {matcherOp(m)}
                  {m.value}
                </Chip>
              ))}
            </div>
          </div>
          {canExpire && s.dash && (
            <Button variant="outline" size="sm" onClick={() => ui.openExpire(s)}>
              <ShieldOff aria-hidden="true" />
              Expire
            </Button>
          )}
        </div>
        {s.comment && <p className="text-sm text-fg">{s.comment}</p>}
        <p className="text-xs text-muted">
          by <span className="text-fg">{s.createdBy || "unknown"}</span> · from{" "}
          {formatDateTime(s.startsAt)} until {formatDateTime(s.endsAt)}
        </p>
      </Panel>
    </motion.div>
  );
}

const TABS = ["firing", "silences"] as const;

export function AlertsPage() {
  const env = useTopic("alertmanager.alerts");
  const data = env?.data;
  // The controls exist while the plugin lists the action (actions.enabled) and the role may run
  // it; an alert cannot be silenced without a duration to offer.
  const silenceAvailable = useActionAvailable("alertmanager", "silence.create");
  const canExpire = useActionAvailable("alertmanager", "silence.expire");
  const canSilence = silenceAvailable && (data?.silenceDurations?.length ?? 0) > 0;
  const now = useNow(30_000).getTime();
  const [tabParam, setTab] = useQueryParam("tab", "firing");
  const tab = (TABS as readonly string[]).includes(tabParam) ? tabParam : "firing";

  const { firing, suppressed, silences } = useMemo(() => {
    const alerts = data?.alerts ?? [];
    return {
      firing: alerts.filter((a) => !isSuppressed(a)),
      suppressed: alerts.filter(isSuppressed),
      silences: (data?.silences ?? [])
        .filter((s) => s.state === "active" || s.state === "pending")
        .sort(
          (a, b) =>
            Number(a.state === "pending") - Number(b.state === "pending") ||
            Date.parse(a.endsAt) - Date.parse(b.endsAt),
        ),
    };
  }, [data]);

  if (!data) {
    return (
      <Page>
        <PanelSkeleton lines={3} />
        <PanelSkeleton lines={2} />
      </Page>
    );
  }

  return (
    <Page>
      <Tabs value={tab} onValueChange={setTab} className="gap-4">
        <Reveal index={0} className="flex flex-wrap items-center gap-3">
          <TabsList variant="line">
            <TabsTrigger value="firing">
              Firing
              <span
                className={cn(
                  "num text-xs",
                  firing.length > 0 ? "font-medium text-crit" : "text-muted",
                )}
              >
                {firing.length}
              </span>
            </TabsTrigger>
            <TabsTrigger value="silences">
              Silences
              <span className="num text-xs text-muted">{silences.length}</span>
            </TabsTrigger>
          </TabsList>
          <SourceChips sources={env?.sources} />
        </Reveal>
        <TabsContent value="firing" className="space-y-4">
          <Reveal index={1}>
            <FiringList alerts={firing} now={now} canSilence={canSilence} />
          </Reveal>
          <Reveal index={2}>
            <Suppressed alerts={suppressed} all={data.alerts} silences={data.silences} now={now} />
          </Reveal>
        </TabsContent>
        <TabsContent value="silences">
          <Reveal index={1}>
            {silences.length === 0 ? (
              <Panel>
                <Empty>No active or pending silences.</Empty>
              </Panel>
            ) : (
              <div className="grid gap-3 xl:grid-cols-2">
                <AnimatePresence initial={false}>
                  {silences.map((s) => (
                    <SilenceCard key={s.id} silence={s} now={now} canExpire={canExpire} />
                  ))}
                </AnimatePresence>
              </div>
            )}
          </Reveal>
        </TabsContent>
      </Tabs>
    </Page>
  );
}
