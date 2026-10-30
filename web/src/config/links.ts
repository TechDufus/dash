const ICON_BASE = "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png";

export interface InfraLink {
  name: string;
  href: string;
  icon: string;
  description: string;
}

/** Infrastructure launcher entries (not Kubernetes apps). */
export const INFRA_LINKS: InfraLink[] = [
  {
    name: "Proxmox",
    href: "https://proxmox.home.techdufus.com:8006",
    icon: `${ICON_BASE}/proxmox.png`,
    description: "Hypervisor",
  },
  {
    name: "iDRAC",
    href: "https://idrac-730.home.techdufus.com",
    icon: `${ICON_BASE}/idrac.png`,
    description: "R730 out-of-band",
  },
  {
    name: "UniFi",
    href: "https://10.0.0.1",
    icon: `${ICON_BASE}/unifi.png`,
    description: "Network",
  },
  {
    name: "UNAS",
    href: "https://unas.home.techdufus.com",
    icon: `${ICON_BASE}/unifi-drive.png`,
    description: "NAS",
  },
  {
    name: "GitHub homelab",
    href: "https://github.com/TechDufus/homelab",
    icon: `${ICON_BASE}/github.png`,
    description: "GitOps repo",
  },
  {
    name: "GitHub dash",
    href: "https://github.com/TechDufus/dash",
    icon: `${ICON_BASE}/github.png`,
    description: "This dashboard",
  },
];
