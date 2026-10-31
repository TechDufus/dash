import { useUIConfig } from "@/api/queries";
import type { LauncherLink } from "@/api/types";

const ICON_BASE = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png";

/** A launcher entry with its icon resolved to an image URL. */
export type InfraLink = LauncherLink;

const NO_LINKS: InfraLink[] = [];

/** A dashboard-icons name ("proxmox") becomes its PNG; a URL or path is used as is. */
export function iconSrc(icon: string): string {
  if (icon === "" || icon.includes("/")) return icon;
  return `${ICON_BASE}/${icon}.png`;
}

/** Infrastructure launcher entries (not Kubernetes apps), from DASH_LAUNCHER_FILE. */
export function useInfraLinks(): InfraLink[] {
  const { data } = useUIConfig();
  return data?.launcher.map((l) => ({ ...l, icon: iconSrc(l.icon) })) ?? NO_LINKS;
}
