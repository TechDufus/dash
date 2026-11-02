// Plugin rows for tests: the eleven shims as /api/v1/plugins reports them (internal/topics/legacy.go).
import type { PluginInfo } from "@/api/types";
import { pluginInfo } from "./harness";

const nav = (path: string, label: string, title: string, icon: string) => [
  { path, label, title, icon },
];

/** The shim rows in display order, all healthy. */
export function shimRows(): PluginInfo[] {
  return [
    pluginInfo({ id: "prometheus", name: "Prometheus-compatible" }),
    pluginInfo({
      id: "alertmanager",
      name: "Alertmanager",
      topics: ["alertmanager.alerts"],
      nav: nav("/alerts", "Alerts", "Alerts and silences", "BellRing"),
    }),
    pluginInfo({
      id: "gatus",
      name: "Gatus",
      topics: ["gatus.status"],
      nav: nav("/status", "Status", "Service status", "Activity"),
    }),
    pluginInfo({ id: "victorialogs", name: "VictoriaLogs" }),
    pluginInfo({
      id: "proxmox",
      name: "Proxmox VE",
      topics: ["proxmox.vms", "proxmox.node", "proxmox.storage"],
      nav: nav("/vms", "VMs", "Virtual machines", "Server"),
    }),
    pluginInfo({
      id: "hardware",
      name: "Hardware",
      topics: ["host", "hardware.machines"],
      nav: nav("/hardware", "Hardware", "Hardware", "Cpu"),
    }),
    pluginInfo({
      id: "kubernetes",
      name: "Kubernetes",
      topics: ["cluster", "storage", "apps"],
      nav: nav("/cluster", "Cluster", "Kubernetes cluster", "Boxes"),
    }),
    pluginInfo({
      id: "flux",
      name: "Flux",
      topics: ["flux"],
      nav: nav("/gitops", "GitOps", "GitOps (Flux)", "GitBranch"),
    }),
    pluginInfo({ id: "grafana", name: "Grafana" }),
    pluginInfo({ id: "headlamp", name: "Headlamp" }),
    pluginInfo({ id: "launcher", name: "Launcher", topics: ["launcher.links"] }),
  ];
}

/** `rows` with the plugin `id` in `state` (and `message`). */
export function withState(
  rows: PluginInfo[],
  id: string,
  state: string,
  message = "",
): PluginInfo[] {
  return rows.map((p) => (p.id === id ? { ...p, state, message } : p));
}

/** The labels the rail shows today, in order. */
export const RAIL_LABELS = [
  "Overview",
  "VMs",
  "Cluster",
  "GitOps",
  "Hardware",
  "Storage",
  "Alerts",
  "Status",
  "Apps",
] as const;
