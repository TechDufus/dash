import { useMemo } from "react";
import { useMe } from "@/api/queries";
import { useTopicData } from "@/api/stream";
import type { LauncherLink } from "@/api/types";

/** A launcher entry with its icon resolved to an image URL ("" when it has none to show). */
export type InfraLink = LauncherLink;

const NO_LINKS: InfraLink[] = [];

// A dashboard-icons name: "proxmox", "unifi-drive", "home-assistant".
const ICON_NAME = /^\w[\w.-]*$/;

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
 * - a name ("proxmox") becomes `<base>/<name>.png`, and shows nothing without a base;
 * - an absolute URL is used only when it lies under the base;
 * - anything else (another origin, a protocol-relative or relative path, a data: or javascript:
 *   URL) shows nothing.
 *
 * Nothing is requested from a third-party host unless the operator set the base.
 */
export function iconSrc(token: string, iconBaseURL: string): string {
  const t = token.trim();
  if (t === "") return "";
  const base = parseBase(iconBaseURL);
  if (t.startsWith("/")) {
    // "//host/x" and "/\host/x" are protocol-relative to a browser, not same-origin paths.
    return t[1] === "/" || t.includes("\\") ? "" : t;
  }
  if (ICON_NAME.test(t)) {
    return base ? `${base.href.replace(/\/+$/, "")}/${encodeURIComponent(t)}.png` : "";
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
  // The icon base arrives with /me; until then every icon is a letter tile.
  const base = useMe().data?.ui?.iconBaseUrl ?? "";
  return useMemo(
    () =>
      data === undefined
        ? { links: NO_LINKS, pending: true }
        : { links: data.links.map((l) => ({ ...l, icon: iconSrc(l.icon, base) })), pending: false },
    [data, base],
  );
}
