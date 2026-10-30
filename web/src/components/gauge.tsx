import { ratioHealth } from "@/lib/health";
import { cn } from "@/lib/utils";

interface GaugeProps {
  /** 0..1 fill. */
  value: number;
  size?: number;
  strokeWidth?: number;
  /** Text at the centre; defaults to the percent. */
  display?: string;
  /** Small caption under the centre value. */
  caption?: string;
  /** When false the arc stays accent coloured instead of switching at 80/90 %. */
  thresholds?: boolean;
  className?: string;
  label?: string;
}

/** 270° SVG arc; the colour switches to warn at 80 % and crit at 90 % when thresholds is on. */
export function Gauge({
  value,
  size = 84,
  strokeWidth = 7,
  display,
  caption,
  thresholds = true,
  className,
  label,
}: GaugeProps) {
  const v = Math.min(1, Math.max(0, Number.isFinite(value) ? value : 0));
  const r = (size - strokeWidth) / 2;
  const c = 2 * Math.PI * r;
  const arc = c * 0.75;
  const health = ratioHealth(v);
  const color = !thresholds || health === "ok" ? "var(--color-accent)" : `var(--color-${health})`;
  const text = display ?? `${Math.round(v * 100)}`;
  return (
    <div
      className={cn("relative inline-flex shrink-0 items-center justify-center", className)}
      style={{ width: size, height: size }}
    >
      <svg
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        role="img"
        aria-label={label ?? `${text}${caption ? ` ${caption}` : ""}`}
      >
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          stroke="var(--color-line)"
          strokeWidth={strokeWidth}
          strokeLinecap="round"
          strokeDasharray={`${arc} ${c}`}
          transform={`rotate(135 ${size / 2} ${size / 2})`}
        />
        <circle
          cx={size / 2}
          cy={size / 2}
          r={r}
          fill="none"
          stroke={color}
          strokeWidth={strokeWidth}
          strokeLinecap="round"
          strokeDasharray={`${arc * v} ${c}`}
          transform={`rotate(135 ${size / 2} ${size / 2})`}
          style={{
            transition: "stroke-dasharray 600ms ease, stroke 300ms ease",
            filter: `drop-shadow(0 0 4px ${color})`,
          }}
        />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center leading-none">
        <span className="num text-sm font-medium text-fg" style={{ fontSize: size * 0.22 }}>
          {text}
        </span>
        {caption && (
          <span className="mt-0.5 text-muted" style={{ fontSize: Math.max(9, size * 0.12) }}>
            {caption}
          </span>
        )}
      </div>
    </div>
  );
}
