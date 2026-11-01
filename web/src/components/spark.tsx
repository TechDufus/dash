import { useId, useMemo } from "react";
import { Area, AreaChart, Tooltip, XAxis, YAxis } from "recharts";
import { type SeriesName, type SeriesParams, type SeriesRange, useSeries } from "@/api/queries";
import { ChartContainer } from "@/components/ui/chart";
import { cn } from "@/lib/utils";

interface SparkProps {
  /** [unix seconds, value] pairs. */
  points: number[][] | undefined;
  color?: string;
  format?: (v: number) => string;
  className?: string;
}

interface TipProps {
  active?: boolean;
  payload?: { value?: number; payload?: { t: number } }[];
  format: (v: number) => string;
}

function SparkTip({ active, payload, format }: TipProps) {
  const p = payload?.[0];
  if (!active || p?.value == null) return null;
  return (
    <div className="num rounded-md border border-line bg-popover px-2 py-1 text-xs text-fg shadow-lg">
      {format(p.value)}
    </div>
  );
}

/** Glowing monotone sparkline (100 % x 40 px), no axes. */
export function Spark({ points, color = "var(--color-accent)", format, className }: SparkProps) {
  const uid = useId().replace(/:/g, "");
  const rows = useMemo(() => (points ?? []).map(([t, v]) => ({ t: t ?? 0, v: v ?? 0 })), [points]);
  const domain = useMemo<[number, number]>(() => {
    if (rows.length === 0) return [0, 1];
    let lo = Number.POSITIVE_INFINITY;
    let hi = Number.NEGATIVE_INFINITY;
    for (const r of rows) {
      if (r.v < lo) lo = r.v;
      if (r.v > hi) hi = r.v;
    }
    const pad = (hi - lo) * 0.15 || Math.abs(hi) * 0.05 || 1;
    return [lo - pad, hi + pad];
  }, [rows]);
  const fmt = format ?? ((v: number) => v.toFixed(1));
  return (
    <ChartContainer
      config={{ v: { label: "value", color } }}
      className={cn("aspect-auto h-10 w-full justify-stretch", className)}
    >
      <AreaChart data={rows} margin={{ top: 4, right: 2, bottom: 2, left: 2 }}>
        <defs>
          <linearGradient id={`g${uid}`} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={color} stopOpacity={0.35} />
            <stop offset="100%" stopColor={color} stopOpacity={0} />
          </linearGradient>
          <filter id={`f${uid}`} x="-10%" y="-40%" width="120%" height="180%">
            <feGaussianBlur stdDeviation="2.5" result="blur" />
            <feMerge>
              <feMergeNode in="blur" />
              <feMergeNode in="SourceGraphic" />
            </feMerge>
          </filter>
        </defs>
        <XAxis dataKey="t" type="number" domain={["dataMin", "dataMax"]} hide />
        <YAxis domain={domain} hide />
        <Tooltip
          cursor={{ stroke: "var(--color-line)" }}
          isAnimationActive={false}
          content={<SparkTip format={fmt} />}
        />
        <Area
          type="monotone"
          dataKey="v"
          stroke={color}
          strokeWidth={1.5}
          fill={`url(#g${uid})`}
          filter={`url(#f${uid})`}
          dot={false}
          activeDot={{ r: 3, fill: color, stroke: "none" }}
          isAnimationActive={false}
        />
      </AreaChart>
    </ChartContainer>
  );
}

interface SparkSeriesProps {
  name: SeriesName;
  range: SeriesRange;
  params?: SeriesParams;
  color?: string;
  format?: (v: number) => string;
  className?: string;
}

/** Spark fed by /api/v1/series (first line of the result). */
export function SparkSeries({ name, range, params, color, format, className }: SparkSeriesProps) {
  const q = useSeries(name, range, params);
  return (
    <Spark points={q.data?.series[0]?.points} color={color} format={format} className={className} />
  );
}
