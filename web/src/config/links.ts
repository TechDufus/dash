import { useMemo } from "react";
import { useMe } from "@/api/queries";
import { useTopicData } from "@/api/stream";
import type { LauncherLink } from "@/api/types";
import { usePluginsState } from "@/plugins/state";

/** A launcher entry with its icon resolved to an image URL ("" when it has none to show). */
export type InfraLink = LauncherLink;

const NO_LINKS: InfraLink[] = [];

// A dashboard-icons name: "proxmox", "unifi-drive", "home-assistant".
const ICON_NAME = /^\w[\w.-]*$/;
// Homepage-style tokens ("mdi-monitor-dashboard", "si-github", "sh-plex") name icon sets that are
// not dashboard-icons: a request for them would be a CDN 404, so they show no icon.
const FOREIGN_SET = /^(mdi|si|sh)-/;
// An image extension a name may carry as written in an annotation ("grafana.png"); the PNG is
// requested either way, so one trailing extension is dropped before ".png" is appended.
const IMAGE_EXT = /\.(png|svg|webp)$/;

/** `base` without trailing slashes as a parsed http(s) URL, or undefined when it is not one. */
function parseBase(base: string): URL | undefined {
  const trimmed = base.trim().replace(/\/+$/, "");
  if (trimmed === "") return undefined;
  try {
    const u = new URL(trimmed);
    return u.protocol === "https:" || u.protocol === "http:" ? u : undefined;
  } catch {
    return undefined;
  }
}

/**
 * The image URL of an icon token a link carries, or "" when there is none to show and the tile
 * falls back to a letter. `iconBaseURL` is ui.iconBaseURL, the only origin an icon may come from:
 *
 * - a same-origin path ("/icons/nas.png") is used as is;
 * - a name ("proxmox") becomes `<base>/<name>.png`, and shows nothing without a base; one trailing
 *   `.png`, `.svg` or `.webp` is dropped first, so "grafana.svg" and "grafana" give the same icon;
 * - a homepage-style name (`mdi-`, `si-`, `sh-` prefix) shows nothing;
 * - an absolute URL is used only when it lies under the base;
 * - anything else (another origin, a protocol-relative or relative path, a data: or javascript:
 *   URL) shows nothing.
 *
 * Nothing is requested from a third-party host unless the operator set the base.
 */
export function iconSrc(token: string, iconBaseURL: string): string {
  const t = token.trim();
  if (t === "" || FOREIGN_SET.test(t)) return "";
  const base = parseBase(iconBaseURL);
  if (t.startsWith("/")) {
    // "//host/x" and "/\host/x" are protocol-relative to a browser, not same-origin paths, and the
    // URL parser drops a tab, LF or CR anywhere: "/\t/host/x" is "//host/x" by the time it is used.
    return t[1] === "/" || /[\t\n\r\\]/.test(t) ? "" : t;
  }
  if (ICON_NAME.test(t)) {
    return base
      ? `${base.href.replace(/\/+$/, "")}/${encodeURIComponent(t.replace(IMAGE_EXT, ""))}.png`
      : "";
  }
  if (!base) return "";
  let u: URL;
  try {
    u = new URL(t);
  } catch {
    return "";
  }
  const underBase =
    (u.protocol === "https:" || u.protocol === "http:") &&
    u.origin === base.origin &&
    u.pathname.startsWith(`${base.pathname.replace(/\/+$/, "")}/`);
  return underBase ? u.href : "";
}

/**
 * ui.iconBaseURL, which arrives with /me; until then (and when it is unset) every icon is a letter
 * tile, so nothing is requested from a third-party host.
 */
export function useIconBase(): string {
  return useMe().data?.ui?.iconBaseUrl ?? "";
}

export interface InfraLinks {
  links: InfraLink[];
  /** The launcher.links envelope has not arrived: nothing is known yet, which is not "no links". */
  pending: boolean;
}

/**
 * Infrastructure launcher entries (not Kubernetes apps), from the launcher.links topic, with their
 * icons resolved against ui.iconBaseURL.
 */
export function useInfraLinks(): InfraLinks {
  const data = useTopicData("launcher.links");
  const base = useIconBase();
  // A launcher that failed to start never publishes (the supervisor drops its topics): its
  // missing envelope is an answer, not something still on its way.
  const failed = usePluginsState().byId.get("launcher")?.state === "error";
  return useMemo(
    () =>
      data === undefined
        ? { links: NO_LINKS, pending: !failed }
        : { links: data.links.map((l) => ({ ...l, icon: iconSrc(l.icon, base) })), pending: false },
    [data, base, failed],
  );
}
