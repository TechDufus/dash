const IEC = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"] as const;

/** Bytes in IEC units with one decimal (plain integer for bytes). */
export function formatBytes(bytes: number | undefined | null): string {
  if (bytes == null || !Number.isFinite(bytes)) return "–";
  const neg = bytes < 0;
  let v = Math.abs(bytes);
  let i = 0;
  while (v >= 1024 && i < IEC.length - 1) {
    v /= 1024;
    i++;
  }
  const s = i === 0 ? String(Math.round(v)) : v.toFixed(1);
  return `${neg ? "-" : ""}${s} ${IEC[i]}`;
}

/** Bytes per second in IEC units. */
export function formatRate(bytesPerSec: number | undefined | null): string {
  if (bytesPerSec == null || !Number.isFinite(bytesPerSec)) return "–";
  return `${formatBytes(bytesPerSec)}/s`;
}

/** 0..1 ratio as an integer percent. */
export function formatPercent(ratio: number | undefined | null): string {
  if (ratio == null || !Number.isFinite(ratio)) return "–";
  return `${Math.round(ratio * 100)}%`;
}

export function formatCelsius(c: number | undefined | null): string {
  if (c == null || !Number.isFinite(c)) return "–";
  return `${Math.round(c)}°C`;
}

export function formatWatts(w: number | undefined | null): string {
  if (w == null || !Number.isFinite(w)) return "–";
  return `${Math.round(w)} W`;
}

export function formatNumber(n: number | undefined | null, digits = 0): string {
  if (n == null || !Number.isFinite(n)) return "–";
  return n.toLocaleString("en-US", {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  });
}

/** Duration as `3d 4h`, `4h 12m`, `12m` or `42s`. */
export function formatDuration(seconds: number | undefined | null): string {
  if (seconds == null || !Number.isFinite(seconds)) return "–";
  const s = Math.max(0, Math.floor(seconds));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m`;
  return `${s}s`;
}

function toMs(t: string | number | Date | undefined | null): number | null {
  if (t == null || t === "") return null;
  const ms = t instanceof Date ? t.getTime() : typeof t === "number" ? t : Date.parse(t);
  return Number.isFinite(ms) ? ms : null;
}

/** Relative time: `5m ago`, `3d ago`, `in 2h`. */
export function formatRelative(
  t: string | number | Date | undefined | null,
  now = Date.now(),
): string {
  const ms = toMs(t);
  if (ms == null) return "–";
  const diff = Math.round((now - ms) / 1000);
  if (Math.abs(diff) < 5) return "just now";
  const label = formatDuration(Math.abs(diff));
  return diff >= 0 ? `${label} ago` : `in ${label}`;
}

/** Age in seconds since a timestamp, or undefined. */
export function ageSeconds(t: string | undefined | null, now = Date.now()): number | undefined {
  const ms = toMs(t);
  return ms == null ? undefined : Math.max(0, (now - ms) / 1000);
}

export function formatClock(d: Date): string {
  return d.toLocaleTimeString("en-GB", { hour12: false });
}

export function formatDateTime(t: string | undefined | null): string {
  const ms = toMs(t);
  if (ms == null) return "–";
  return new Date(ms).toLocaleString("en-GB", {
    year: "numeric",
    month: "short",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  });
}

/** Shorten an image reference: drop the registry host and any digest. */
export function shortImage(image: string): string {
  const noDigest = image.split("@")[0] ?? image;
  const parts = noDigest.split("/");
  return parts.length > 2 ? parts.slice(1).join("/") : noDigest;
}
