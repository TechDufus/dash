import { useId, useMemo } from "react";
import { Area, AreaChart, CartesianGrid, Tooltip, XAxis, YAxis } from "recharts";
import { ChartContainer } from "@/components/ui/chart";
import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";
import type { Sample } from "./model.gen";

/** The numeric fields of a sample a line can draw. */
export type SampleField = Exclude<keyof Sample, "t">;

export interface ChartLine {
  field: SampleField;
  label: string;
  color: string;
}

interface Row {
  t: number;
  [label: string]: number | null;
}

/**
 * The rows a chart draws: one per sample, and a row without values where the host was away (two
 * samples further apart than three intervals), so the line breaks there instead of bridging it.
 */
export function chartRows(
  samples: readonly Sample[],
  lines: readonly ChartLine[],
  intervalSeconds: number,
): Row[] {
  const gap = Math.max(3 * intervalSeconds, 15);
  const rows: Row[] = [];
  let prev: number | undefined;
  for (const s of samples) {
    if (prev !== undefined && s.t - prev > gap) {
      const hole: Row = { t: prev + 1 };
      for (const l of lines) hole[l.label] = null;
      rows.push(hole);
    }
    const row: Row = { t: s.t };
    for (const l of lines) row[l.label] = s[l.field] ?? null;
    rows.push(row);
    prev = s.t;
  }
  return rows;
}

const clock = (t: number) =>
  new Date(t * 1000).toLocaleTimeString("en-GB", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });

interface TipProps {
  active?: boolean;
  label?: number;
  payload?: { name?: string; value?: number | null; color?: string }[];
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

interface LiveChartProps {
  title: string;
  samples: readonly Sample[];
  lines: readonly ChartLine[];
  intervalSeconds: number;
  format: (v: number) => string;
  /** The history is still being read. */
  pending?: boolean;
  /** Fixed y domain (e.g. [0, 1] for ratios). */
  domain?: [number | "auto", number | "auto"];
  height?: number;
  className?: string;
}

/**
 * A chart of a host's samples, drawn like SeriesChart (axes, grid, gradient areas, tooltip). It
 * is a figure named by its title, with the latest values in words for a screen reader.
 */
export function LiveChart({
  title,
  samples,
  lines,
  intervalSeconds,
  format,
  pending,
  domain,
  height = 180,
  className,
}: LiveChartProps) {
  const uid = useId().replace(/:/g, "");
  const rows = useMemo(
    () => chartRows(samples, lines, intervalSeconds),
    [samples, lines, intervalSeconds],
  );
  const yWidth = useMemo(() => {
    let hi = 0;
    for (const r of rows) for (const l of lines) hi = Math.max(hi, Math.abs(r[l.label] ?? 0));
    const longest = Math.max(format(hi).length, format(hi / 2).length, format(0).length);
    return Math.min(110, Math.max(44, Math.ceil(longest * 6.2) + 10));
  }, [rows, lines, format]);
  const last = samples.at(-1);
  const summary = last
    ? `Latest: ${lines.map((l) => `${l.label} ${last[l.field] == null ? "not reported" : format(last[l.field] as number)}`).join(", ")}.`
    : "No samples yet.";
  return (
    <figure
      aria-labelledby={`${uid}-title`}
      aria-describedby={`${uid}-summary`}
      className={cn("min-w-0 space-y-1.5", className)}
    >
      <figcaption id={`${uid}-title`} className="text-xs font-medium text-muted">
        {title}
      </figcaption>
      <p id={`${uid}-summary`} className="sr-only">
        {summary}
      </p>
      {pending && rows.length === 0 ? (
        <Skeleton style={{ height }} className="w-full" />
      ) : rows.length === 0 ? (
        <div
          style={{ height }}
          className="flex items-center justify-center rounded-lg border border-dashed border-line text-xs text-faint"
        >
          no samples yet
        </div>
      ) : (
        <ChartContainer
          config={Object.fromEntries(
            lines.map((l) => [l.label, { label: l.label, color: l.color }]),
          )}
          className="aspect-auto w-full"
          style={{ height }}
        >
          <AreaChart data={rows} margin={{ top: 6, right: 6, bottom: 0, left: 0 }}>
            <defs>
              {lines.map((l, i) => (
                <linearGradient key={l.label} id={`g${uid}${i}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor={l.color} stopOpacity={0.3} />
                  <stop offset="100%" stopColor={l.color} stopOpacity={0} />
                </linearGradient>
              ))}
            </defs>
            <CartesianGrid vertical={false} stroke="var(--color-line)" />
            <XAxis
              dataKey="t"
              type="number"
              domain={["dataMin", "dataMax"]}
              tickFormatter={clock}
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
            {lines.map((l, i) => (
              <Area
                key={l.label}
                type="monotone"
                dataKey={l.label}
                name={l.label}
                stroke={l.color}
                strokeWidth={1.5}
                fill={`url(#g${uid}${i})`}
                dot={false}
                connectNulls={false}
                isAnimationActive={false}
              />
            ))}
          </AreaChart>
        </ChartContainer>
      )}
    </figure>
  );
}
