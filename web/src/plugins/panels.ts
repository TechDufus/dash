import { panels as alertmanager } from "./modules/alertmanager/panels";
import { panels as flux } from "./modules/flux/panels";
import { panels as hardware } from "./modules/hardware/panels";
import { panels as kubernetes } from "./modules/kubernetes/panels";
import { panels as launcher } from "./modules/launcher/panels";
import { panels as proxmox } from "./modules/proxmox/panels";
import type { ModulePanels } from "./types";

/** The panels each module contributes to the composite pages (P2.8), by module id. */
export const PANELS: Readonly<Record<string, ModulePanels | undefined>> = {
  alertmanager,
  flux,
  hardware,
  kubernetes,
  launcher,
  proxmox,
};
