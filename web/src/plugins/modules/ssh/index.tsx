import { Server } from "lucide-react";
import { lazy } from "react";
import { definePlugin, type PaletteGroup, type RouteProps, topic } from "../../types";
import type { HostsData } from "./model.gen";

const HostsPage = lazy(() =>
  import("./hosts-page").then((m) => ({
    default: ({ params }: RouteProps) => <m.HostsPage host={params.host} />,
  })),
);

const SSHSettings = lazy(() => import("./ssh-settings").then((m) => ({ default: m.SSHSettings })));

export default definePlugin({
  id: "ssh",
  order: 45,
  topics: {
    "ssh.hosts": topic<HostsData>(),
  },
  routes: [{ path: "/hosts/:host?", page: HostsPage }],
  palette: (ctx): readonly PaletteGroup[] => [
    {
      id: "hosts",
      heading: "Hosts",
      items: (ctx.topic("ssh.hosts")?.hosts ?? []).map((h) => ({
        id: `host:${h.name}`,
        value: `host ${h.name} ${h.info?.os ?? ""}`,
        label: h.name,
        hint: h.info?.os || undefined,
        icon: Server,
        onSelect: () => ctx.go(`/hosts/${encodeURIComponent(h.name)}`),
      })),
    },
  ],
  settings: SSHSettings,
});
