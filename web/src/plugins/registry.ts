import { Database, LayoutDashboard, LayoutGrid, type LucideIcon } from "lucide-react";
import alertmanager from "./modules/alertmanager";
import cnpg from "./modules/cnpg";
import flux from "./modules/flux";
import gatus from "./modules/gatus";
import grafana from "./modules/grafana";
import hardware from "./modules/hardware";
import headlamp from "./modules/headlamp";
import kubernetes from "./modules/kubernetes";
import launcher from "./modules/launcher";
import proxmox from "./modules/proxmox";
import rustfs from "./modules/rustfs";
import type { PluginModule } from "./types";

/**
 * The only list of SPA modules, mirroring cmd/dash/plugins.go. A module says how to render a
 * plugin; the backend (/api/v1/plugins) says what exists.
 */
export const MODULES = [
  alertmanager,
  gatus,
  proxmox,
  hardware,
  kubernetes,
  flux,
  cnpg,
  rustfs,
  launcher,
  grafana,
  headlamp,
] as const;

export type ModuleId = (typeof MODULES)[number]["id"];

export const moduleById: ReadonlyMap<string, PluginModule> = new Map(
  MODULES.map((m) => [m.id, m as PluginModule]),
);

/** Rail position of a module without an `order`: after every numbered entry, in registry order. */
export function moduleOrder(m: PluginModule): number {
  return m.order ?? 1000 + MODULES.findIndex((x) => x.id === m.id);
}

/** A page that is always there: composite pages whose panels come from plugins. */
export interface CorePage {
  key: string;
  path: string;
  label: string;
  title: string;
  icon: LucideIcon;
  order: number;
}

export const CORE_PAGES: readonly CorePage[] = [
  {
    key: "core:overview",
    path: "/",
    label: "Overview",
    title: "Overview",
    icon: LayoutDashboard,
    order: 0,
  },
  {
    key: "core:storage",
    path: "/storage",
    label: "Storage",
    title: "Storage",
    icon: Database,
    order: 50,
  },
  { key: "core:apps", path: "/apps", label: "Apps", title: "Apps", icon: LayoutGrid, order: 80 },
];
