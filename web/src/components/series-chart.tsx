import { useId, useMemo } from "react";
import { Area, AreaChart, CartesianGrid, Tooltip, XAxis, YAxis } from "recharts";
import { type SeriesName, type SeriesParams, type SeriesRange, useSeries } from "@/api/queries";
import type { Series } from "@/api/types";
import { ChartContainer } from "@/components/ui/chart";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

const PALETTE = [
  "var(--color-accent)",
  "var(--color-ok)",
  "var(--color-warn)",
  "#a78bfa",
  "#f472b6",
  "#60a5fa",
  "#fb923c",
];

/** A short legend name for one series line. */
export function lineName(labels: Record<string, string>): string {
  return (
    labels.name ??
    labels.sensor ??
    labels.pod ??
    labels.instance ??
    (Object.values(labels).join(" ") || "value")
  );
}

interface Row {
  t: number;
  [key: string]: number;
}

export function seriesRows(series: Series | undefined): { rows: Row[]; keys: string[] } {
  if (!series) return { rows: [], keys: [] };
  const byT = new Map<number, Row>();
  const keys: string[] = [];
  for (const line of series.series) {
    let key = lineName(line.labels);
    while (keys.includes(key)) key += "'";
    keys.push(key);
    for (const [t, v] of line.points) {
      if (t === undefined || v === undefined) continue;
      let row = byT.get(t);
      if (!row) {
        row = { t };
        byT.set(t, row);
      }
      row[key] = v;
    }
  }
  return { rows: [...byT.values()].sort((a, b) => a.t - b.t), keys };
}

function timeTick(range: SeriesRange) {
  return (t: number) => {
    const d = new Date(t * 1000);
    if (range === "7d" || range === "24h") {
      return range === "7d"
        ? d.toLocaleDateString("en-GB", { day: "2-digit", month: "short" })
        : d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false });
    }
    return d.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit", hour12: false });
  };
}

interface TipProps {
  active?: boolean;
  label?: number;
  payload?: { name?: string; value?: number; color?: string }[];
  format: (v: number) => string;
}

function Tip({ active, label, payload, format }: TipProps) {
  if (!active || !payload?.length) return null;
  return (
    <div className="rounded-md border border-line bg-popover px-2.5 py-1.5 text-xs shadow-lg">
      <div className="num mb-1 text-muted">
        {label ? new Date(label * 1000).toLocaleString("en-GB", { hour12: false }) : ""}
      </div>
      {payload.map((p) => (
        <div key={p.name} className="flex items-center gap-2">
          <span className="size-2 rounded-full" style={{ background: p.color }} />
          <span className="text-muted">{p.name}</span>
          <span className="num ml-auto pl-3 text-fg">
            {p.value == null ? "–" : format(p.value)}
          </span>
        </div>
      ))}
    </div>
  );
}

interface SeriesChartProps {
  name: SeriesName;
  range: SeriesRange;
  params?: SeriesParams;
  format: (v: number) => string;
  title?: string;
  height?: number;
  /** Fixed y domain (e.g. [0, 1] for ratios). */
  domain?: [number | "auto", number | "auto"];
  className?: string;
  enabled?: boolean;
}

/** Full chart (axes, grid, tooltip) over /api/series. */
export function SeriesChart({
  name,
  range,
  params,
  format,
  title,
  height = 160,
  domain,
  className,
  enabled = true,
}: SeriesChartProps) {
  const uid = useId().replace(/:/g, "");
  const q = useSeries(name, range, params, enabled);
  const { rows, keys } = useMemo(() => seriesRows(q.data), [q.data]);
  const tick = useMemo(() => timeTick(range), [range]);
  const yWidth = useMemo(() => {
    let hi = 0;
    for (const r of rows) for (const k of keys) hi = Math.max(hi, Math.abs(r[k] ?? 0));
    const longest = Math.max(format(hi).length, format(hi / 2).length, format(0).length);
    return Math.min(110, Math.max(44, Math.ceil(longest * 6.2) + 10));
  }, [rows, keys, format]);
  return (
    <div className={cn("space-y-1.5", className)}>
      {title && <div className="text-xs font-medium text-muted">{title}</div>}
      {q.isPending ? (
        <Skeleton style={{ height }} className="w-full" />
      ) : rows.length === 0 ? (
        <div
          style={{ height }}
          className="flex items-center justify-center rounded-lg border border-dashed border-line text-xs text-faint"
        >
          {q.isError ? "metrics unavailable" : "no data"}
        </div>
      ) : (
        <ChartContainer
          config={Object.fromEntries(
            keys.map((k, i) => [k, { label: k, color: PALETTE[i % PALETTE.length] }]),
          )}
          className="aspect-auto w-full"
          style={{ height }}
        >
          <AreaChart data={rows} margin={{ top: 6, right: 6, bottom: 0, left: 0 }}>
            <defs>
              {keys.map((k, i) => (
                <linearGradient key={k} id={`g${uid}${i}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={PALETTE[i % PALETTE.length]} stopOpacity={0.3} />
                  <stop offset="100%" stopColor={PALETTE[i % PALETTE.length]} stopOpacity={0} />
                </linearGradient>
              ))}
            </defs>
            <CartesianGrid vertical={false} stroke="var(--color-line)" />
            <XAxis
              dataKey="t"
              type="number"
              domain={["dataMin", "dataMax"]}
              tickFormatter={tick}
              tickLine={false}
              axisLine={false}
              minTickGap={48}
              tick={{ fill: "var(--color-faint)", fontSize: 10 }}
            />
            <YAxis
              width={yWidth}
              domain={domain ?? ["auto", "auto"]}
              tickFormatter={format}
              tickLine={false}
              axisLine={false}
              tick={{ fill: "var(--color-faint)", fontSize: 10 }}
            />
            <Tooltip
              isAnimationActive={false}
              cursor={{ stroke: "var(--color-line)" }}
              content={<Tip format={format} />}
            />
            {keys.map((k, i) => (
              <Area
                key={k}
                type="monotone"
                dataKey={k}
                name={k}
                stroke={PALETTE[i % PALETTE.length]}
                strokeWidth={1.5}
                fill={`url(#g${uid}${i})`}
                dot={false}
                connectNulls
                isAnimationActive={false}
              />
            ))}
          </AreaChart>
        </ChartContainer>
      )}
    </div>
  );
}
