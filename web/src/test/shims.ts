// Plugin rows for tests: the built-in plugins as /api/v1/plugins reports them (their own descriptors).
import type { ActionInfo, PluginInfo } from "@/api/types";
import { pluginInfo } from "./harness";

const nav = (path: string, label: string, title: string, icon: string) => [
  { path, label, title, icon },
];

/** A framework action row as /api/v1/plugins lists it. */
const action = (name: string, minRole: string): ActionInfo => ({
  name,
  title: name[0].toUpperCase() + name.slice(1),
  destructive: true,
  idempotent: false,
  confirm: "none",
  minRole,
  inputSchema: null,
});

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
      topics: ["proxmox.vms", "proxmox.cluster", "proxmox.storage"],
      nav: nav("/vms", "VMs", "Virtual machines", "Server"),
    }),
    pluginInfo({
      id: "hardware",
      name: "Hardware",
      topics: ["hardware.hosts", "hardware.machines"],
      nav: nav("/hardware", "Hardware", "Hardware", "Cpu"),
    }),
    pluginInfo({
      id: "kubernetes",
      name: "Kubernetes",
      topics: ["kubernetes.cluster", "kubernetes.storage", "kubernetes.apps"],
      nav: nav("/cluster", "Cluster", "Kubernetes cluster", "Boxes"),
      actions: [action("restart", "admin")],
    }),
    pluginInfo({
      id: "flux",
      name: "Flux",
      topics: ["flux.objects"],
      nav: nav("/gitops", "GitOps", "GitOps (Flux)", "GitBranch"),
      actions: [action("reconcile", "admin")],
    }),
    pluginInfo({ id: "cnpg", name: "CloudNativePG", topics: ["cnpg.clusters"] }),
    pluginInfo({ id: "rustfs", name: "RustFS", topics: ["rustfs.usage"] }),
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
