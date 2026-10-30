import {
  Activity,
  BellRing,
  Boxes,
  Cpu,
  Database,
  GitBranch,
  LayoutDashboard,
  LayoutGrid,
  type LucideIcon,
  Server,
} from "lucide-react";

export interface NavItem {
  to: string;
  label: string;
  title: string;
  icon: LucideIcon;
}

export const NAV: NavItem[] = [
  { to: "/", label: "Overview", title: "Overview", icon: LayoutDashboard },
  { to: "/vms", label: "VMs", title: "Virtual machines", icon: Server },
  { to: "/cluster", label: "Cluster", title: "Kubernetes cluster", icon: Boxes },
  { to: "/gitops", label: "GitOps", title: "GitOps (Flux)", icon: GitBranch },
  { to: "/hardware", label: "Hardware", title: "Hardware (R730)", icon: Cpu },
  { to: "/storage", label: "Storage", title: "Storage", icon: Database },
  { to: "/alerts", label: "Alerts", title: "Alerts and silences", icon: BellRing },
  { to: "/status", label: "Status", title: "Service status", icon: Activity },
  { to: "/apps", label: "Apps", title: "Apps", icon: LayoutGrid },
];

export function isActive(item: NavItem, location: string): boolean {
  return item.to === "/"
    ? location === "/"
    : location === item.to || location.startsWith(`${item.to}/`);
}

export function titleFor(location: string): string {
  return NAV.find((n) => isActive(n, location))?.title ?? "dash";
}
