import { Activity, Search } from "lucide-react";
import { useMemo } from "react";
import { useTopic } from "@/api/stream";
import type { CheckResult, Endpoint } from "@/api/types";
import { Page, Reveal } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton, SourceChips } from "@/components/panel";
import { HealthBadge } from "@/components/status";
import { Input } from "@/components/ui/input";
import { formatClock, formatDateTime, formatRelative } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import { useQueryParam } from "@/lib/query-param";
import { cn } from "@/lib/utils";

const SEGMENTS = 30;

function urlHost(url: string): string {
  try {
    return new URL(url).host || url;
  } catch {
    return url;
  }
}

/** Last 30 results, oldest first (Gatus order is not relied on). */
function recent(results: CheckResult[]): CheckResult[] {
  return [...results]
    .sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp))
    .slice(-SEGMENTS);
}

/** The infrastructure group first, then the rest alphabetically. */
function groupOrder(a: string, b: string): number {
  const ai = a.toLowerCase().includes("infra") ? 0 : 1;
  const bi = b.toLowerCase().includes("infra") ? 0 : 1;
  return ai - bi || a.localeCompare(b);
}

/** 30 thin segments: a success is a short ok bar, a failure a taller crit bar with a top cap. */
function ResultBar({ results, name }: { results: CheckResult[]; name: string }) {
  const last = recent(results);
  const failed = last.filter((r) => !r.success).length;
  const pad = SEGMENTS - last.length;
  return (
    <div
      role="img"
      aria-label={`${name}: ${last.length - failed} of ${last.length} recent checks succeeded`}
      className="flex h-6 items-end gap-px"
    >
      {Array.from({ length: pad }, (_, i) => (
        <span
          // biome-ignore lint/suspicious/noArrayIndexKey: static padding before the first result
          key={`pad-${i}`}
          className="h-2 w-1 rounded-[1px] bg-line"
        />
      ))}
      {last.map((r) => (
        <span
          key={r.timestamp}
          title={`${r.success ? "Success" : "Failure"} · ${formatClock(new Date(r.timestamp))} ${formatDateTime(r.timestamp)} · ${Math.round(r.durationMs)} ms`}
          className={cn(
            "w-1 rounded-[1px]",
            r.success ? "h-3.5 bg-ok/80" : "h-6 border-t-2 border-t-fg bg-crit",
          )}
        />
      ))}
    </div>
  );
}

function EndpointRow({ e, now }: { e: Endpoint; now: number }) {
  const results = recent(e.results);
  const avg = results.length
    ? results.reduce((sum, r) => sum + r.durationMs, 0) / results.length
    : undefined;
  const lastCheck = results[results.length - 1]?.timestamp;
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-3">
      <div className="min-w-[180px] flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium text-fg">{e.name}</span>
          <HealthBadge health={e.healthy ? "ok" : "crit"} label={e.healthy ? "Up" : "Down"} />
        </div>
        <div className="truncate text-xs text-muted" title={e.url}>
          {urlHost(e.url)}
        </div>
      </div>
      <ResultBar results={e.results} name={e.name} />
      <div className="flex w-[10.5rem] items-baseline justify-between gap-3 text-xs">
        <span className="num text-fg" title="Average duration of the shown checks">
          {avg === undefined ? "–" : `${Math.round(avg)} ms`}
        </span>
        <span className="text-muted" title={formatDateTime(lastCheck)}>
          {lastCheck ? formatRelative(lastCheck, now) : "never"}
        </span>
      </div>
    </div>
  );
}

export function StatusPage() {
  const env = useTopic("status");
  const data = env?.data;
  const now = useNow(15_000).getTime();
  const [q, setQ] = useQueryParam("q");
  const needle = q.trim().toLowerCase();

  const { groups, total, healthy } = useMemo(() => {
    const all = data?.endpoints ?? [];
    const byGroup = new Map<string, Endpoint[]>();
    for (const e of all) {
      if (needle && !`${e.group} ${e.name} ${e.url}`.toLowerCase().includes(needle)) continue;
      const list = byGroup.get(e.group) ?? [];
      list.push(e);
      byGroup.set(e.group, list);
    }
    const groups = [...byGroup.entries()]
      .sort(([a], [b]) => groupOrder(a, b))
      .map(([name, items]) => ({
        name,
        items: items.sort(
          (a, b) => Number(a.healthy) - Number(b.healthy) || a.name.localeCompare(b.name),
        ),
      }));
    return { groups, total: all.length, healthy: all.filter((e) => e.healthy).length };
  }, [data, needle]);

  if (!data) {
    return (
      <Page>
        <PanelSkeleton lines={1} />
        <PanelSkeleton lines={4} />
        <PanelSkeleton lines={4} />
      </Page>
    );
  }

  return (
    <Page>
      <Reveal index={0} className="flex flex-wrap items-center gap-3">
        <div className="relative w-full sm:w-72">
          <Search
            className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted"
            aria-hidden="true"
          />
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Filter endpoints"
            aria-label="Filter endpoints"
            className="pl-8"
          />
        </div>
        <span className="num text-xs text-muted">
          <span className={cn("text-sm font-medium", healthy === total ? "text-ok" : "text-crit")}>
            {healthy}/{total}
          </span>{" "}
          endpoints healthy
        </span>
        <SourceChips sources={env?.sources} />
      </Reveal>
      {groups.length === 0 ? (
        <Reveal index={1}>
          <Panel>
            <Empty>{total === 0 ? "No endpoints reported." : "Nothing matches."}</Empty>
          </Panel>
        </Reveal>
      ) : (
        groups.map((g, i) => {
          const up = g.items.filter((e) => e.healthy).length;
          return (
            <Reveal key={g.name} index={i + 1}>
              <Panel>
                <PanelHeader title={g.name || "Ungrouped"} icon={<Activity />}>
                  <HealthBadge
                    health={up === g.items.length ? "ok" : "crit"}
                    label={`${up}/${g.items.length} healthy`}
                  />
                </PanelHeader>
                <div className="divide-y divide-line border-t border-line">
                  {g.items.map((e) => (
                    <EndpointRow key={e.key} e={e} now={now} />
                  ))}
                </div>
              </Panel>
            </Reveal>
          );
        })
      )}
    </Page>
  );
}
