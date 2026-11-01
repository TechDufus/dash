import { Radio, RefreshCw, Search } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { errorMessage } from "@/api/client";
import { type LogSince, useLogs } from "@/api/queries";
import type { LogLine } from "@/api/types";
import { Empty } from "@/components/panel";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { formatClock } from "@/lib/format";
import { useDebounced } from "@/lib/hooks";
import { cn } from "@/lib/utils";

/** Lines kept while tailing. */
const MAX_LIVE_LINES = 1000;
/** Live lines are flushed to state at most this often. */
const FLUSH_MS = 100;
const ALL = "all";

const SINCE_OPTIONS: { value: LogSince; label: string }[] = [
  { value: "5m", label: "Last 5 min" },
  { value: "15m", label: "Last 15 min" },
  { value: "1h", label: "Last hour" },
  { value: "6h", label: "Last 6 hours" },
  { value: "24h", label: "Last 24 hours" },
];

type StreamState = "connecting" | "streaming" | "reconnecting" | "closed";

interface Row {
  id: number;
  line: LogLine;
}

function shortTime(time: string): string {
  const ms = Date.parse(time);
  return Number.isFinite(ms) ? formatClock(new Date(ms)) : time;
}

interface PodLogsProps {
  ns: string;
  pod: string;
  containers: string[];
  /** The Logs tab is visible and the sheet is open. */
  active: boolean;
}

export function PodLogs({ ns, pod, containers, active }: PodLogsProps) {
  const [container, setContainer] = useState(ALL);
  const [since, setSince] = useState<LogSince>("15m");
  const [search, setSearch] = useState("");
  const [live, setLive] = useState(false);
  const q = useDebounced(search.trim(), 300);
  const containerParam = container === ALL ? undefined : container;
  const qParam = q === "" ? undefined : q;

  const logs = useLogs({ ns, pod, container: containerParam, q: qParam, since }, active && !live);
  const [liveRows, setLiveRows] = useState<Row[]>([]);
  const [stream, setStream] = useState<StreamState>("connecting");

  // The API returns lines oldest first, so the newest line ends up at the bottom.
  const fetched = useMemo<Row[]>(
    () => (logs.data ? logs.data.map((line, id) => ({ id, line })) : []),
    [logs.data],
  );
  const rows = live ? liveRows : fetched;

  useEffect(() => {
    if (!active || !live) return;
    setLiveRows([]);
    setStream("connecting");
    const params = new URLSearchParams({ ns, pod });
    if (containerParam) params.set("container", containerParam);
    if (qParam) params.set("q", qParam);
    const es = new EventSource(`/api/v1/plugins/victorialogs/logs/tail?${params.toString()}`);
    let buffer: Row[] = [];
    let nextId = 0;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const flush = () => {
      timer = undefined;
      const batch = buffer;
      buffer = [];
      setLiveRows((prev) => {
        const merged = prev.concat(batch);
        return merged.length > MAX_LIVE_LINES ? merged.slice(-MAX_LIVE_LINES) : merged;
      });
    };
    es.onopen = () => setStream("streaming");
    es.addEventListener("line", (ev) => {
      let line: LogLine;
      try {
        line = JSON.parse((ev as MessageEvent<string>).data) as LogLine;
      } catch {
        return;
      }
      setStream("streaming");
      buffer.push({ id: nextId++, line });
      if (timer === undefined) timer = setTimeout(flush, FLUSH_MS);
    });
    // EventSource retries on its own; a CLOSED socket (e.g. HTTP error) will not.
    es.onerror = () => setStream(es.readyState === EventSource.CLOSED ? "closed" : "reconnecting");
    return () => {
      es.close();
      clearTimeout(timer);
    };
  }, [active, live, ns, pod, containerParam, qParam]);

  // Follow the newest line unless the user scrolled up.
  const scroller = useRef<HTMLDivElement>(null);
  const stick = useRef(true);
  // biome-ignore lint/correctness/useExhaustiveDependencies: re-pin whenever the rows or mode change
  useEffect(() => {
    const el = scroller.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [rows, live]);
  // biome-ignore lint/correctness/useExhaustiveDependencies: a new query or mode starts pinned
  useEffect(() => {
    stick.current = true;
  }, [live, container, since, q]);

  const onScroll = () => {
    const el = scroller.current;
    if (el) stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select value={container} onValueChange={setContainer}>
          <SelectTrigger size="sm" className="w-40" aria-label="Container">
            <SelectValue />
          </SelectTrigger>
          <SelectContent position="popper">
            <SelectItem value={ALL}>All containers</SelectItem>
            {containers.map((c) => (
              <SelectItem key={c} value={c}>
                {c}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={since} onValueChange={(v) => setSince(v as LogSince)} disabled={live}>
          <SelectTrigger
            size="sm"
            className="w-36"
            aria-label="Time range"
            title={live ? "Live tail ignores the time range" : undefined}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent position="popper">
            {SINCE_OPTIONS.map((o) => (
              <SelectItem key={o.value} value={o.value}>
                {o.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <div className="relative min-w-32 flex-1">
          <Search
            className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted"
            aria-hidden="true"
          />
          <Input
            type="search"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search logs"
            aria-label="Search logs"
            className="h-7 pl-8"
          />
        </div>
        <Button
          type="button"
          size="sm"
          variant={live ? "secondary" : "outline"}
          aria-pressed={live}
          onClick={() => setLive((v) => !v)}
        >
          <Radio aria-hidden="true" className={cn(live && "text-ok")} />
          Live
        </Button>
        <Button
          type="button"
          size="icon-sm"
          variant="ghost"
          aria-label="Refresh logs"
          title="Refresh"
          disabled={live || logs.isFetching}
          onClick={() => void logs.refetch()}
        >
          <RefreshCw aria-hidden="true" className={cn(logs.isFetching && "animate-spin")} />
        </Button>
      </div>

      <div
        className="flex items-center justify-between gap-2 text-xs text-muted"
        aria-live="polite"
      >
        <span className="num">{rows.length} lines</span>
        {live && <StreamIndicator state={stream} />}
      </div>

      <div
        ref={scroller}
        onScroll={onScroll}
        className="num min-h-64 flex-1 overflow-auto rounded-lg border border-line bg-canvas/60 p-2 text-xs leading-5"
      >
        {!live && logs.isPending && logs.fetchStatus !== "idle" ? (
          <div className="space-y-1.5">
            {Array.from({ length: 8 }, (_, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: static placeholder rows
              <Skeleton key={i} className="h-4 w-full" />
            ))}
          </div>
        ) : !live && logs.isError ? (
          <Empty className="text-crit">{errorMessage(logs.error)}</Empty>
        ) : rows.length === 0 ? (
          <Empty>{live ? "Waiting for log lines…" : "No log lines in this range"}</Empty>
        ) : (
          rows.map(({ id, line }) => (
            <div key={id} className="flex gap-2 whitespace-pre-wrap break-all">
              <span className="shrink-0 text-faint">{shortTime(line.time)}</span>
              {line.container && <span className="shrink-0 text-accent/80">{line.container}</span>}
              <span className="min-w-0 text-fg">{line.msg}</span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}

function StreamIndicator({ state }: { state: StreamState }) {
  if (state === "streaming") {
    return (
      <span className="inline-flex items-center gap-1.5 text-ok">
        <span className="relative inline-flex size-2">
          <span className="animate-dash-ping absolute inline-flex size-full rounded-full bg-ok opacity-60" />
          <span className="relative inline-flex size-2 rounded-full bg-ok" />
        </span>
        streaming
      </span>
    );
  }
  if (state === "connecting") return <span>connecting…</span>;
  if (state === "reconnecting")
    return <span className="text-warn">connection lost, reconnecting…</span>;
  return <span className="text-crit">stream closed; toggle Live to retry</span>;
}
