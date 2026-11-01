import { Server } from "lucide-react";
import { lazy } from "react";
import type { VMsData } from "@/api/types";
import { definePlugin, type PaletteGroup, type RouteProps, topic } from "../../types";

const VMsPage = lazy(() =>
  import("@/pages/vms").then((m) => ({
    default: ({ params }: RouteProps) => <m.VMsPage vmid={params.vmid} />,
  })),
);

export default definePlugin({
  id: "proxmox",
  order: 10,
  topics: { vms: topic<VMsData>() },
  routes: [{ path: "/vms/:vmid?", page: VMsPage }],
  palette: (ctx): readonly PaletteGroup[] => {
    const vms = ctx.topic("vms")?.vms.filter((v) => !v.template) ?? [];
    return [
      {
        id: "vms",
        heading: "VMs",
        items: vms.map((v) => ({
          id: `vm:${v.vmid}`,
          value: `vm ${v.name} ${v.vmid}`,
          label: v.name,
          hint: String(v.vmid),
          icon: Server,
          onSelect: () => ctx.go(`/vms/${v.vmid}`),
        })),
      },
    ];
  },
  linkIcons: { proxmox: Server },
});
