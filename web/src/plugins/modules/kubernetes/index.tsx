import { Boxes, Container, ExternalLink, Layers, RotateCw } from "lucide-react";
import { lazy } from "react";
import type { AppsData, ClusterData, StorageData } from "@/api/types";
import { ui } from "@/api/ui-store";
import { definePlugin, type PaletteGroup, topic } from "../../types";

const ClusterPage = lazy(() =>
  import("@/pages/cluster").then((m) => ({ default: () => <m.ClusterPage /> })),
);

export default definePlugin({
  id: "kubernetes",
  order: 20,
  topics: {
    cluster: topic<ClusterData>(),
    storage: topic<StorageData>(),
    apps: topic<AppsData>(),
  },
  routes: [{ path: "/cluster/*?", page: ClusterPage }],
  palette: (ctx): readonly PaletteGroup[] => {
    const cluster = ctx.topic("cluster");
    const apps = ctx.topic("apps");
    return [
      {
        id: "actions",
        heading: "Actions",
        items: (cluster?.workloads ?? [])
          .filter((w) => w.restartable)
          .map((w) => ({
            id: `restart:${w.kind}/${w.namespace}/${w.name}`,
            value: `action restart ${w.namespace}/${w.name} ${w.kind}`,
            label: `Restart ${w.namespace}/${w.name}`,
            icon: RotateCw,
            onSelect: () =>
              ui.openRestart({
                kind: w.kind,
                namespace: w.namespace,
                name: w.name,
                protected: w.protected,
              }),
          })),
      },
      {
        id: "nodes",
        heading: "Nodes",
        items: (cluster?.nodes ?? []).map((n) => ({
          id: `node:${n.name}`,
          value: `node ${n.name} ${n.ip}`,
          label: n.name,
          hint: n.ip,
          hintMono: true,
          icon: Boxes,
          onSelect: () => ctx.go("/cluster?tab=nodes"),
        })),
      },
      {
        id: "workloads",
        heading: "Workloads",
        items: (cluster?.workloads ?? []).map((w) => ({
          id: `workload:${w.kind}/${w.namespace}/${w.name}`,
          value: `workload ${w.namespace}/${w.name} ${w.kind}`,
          label: `${w.namespace}/${w.name}`,
          hint: w.kind,
          icon: Layers,
          onSelect: () =>
            ctx.go(
              `/cluster?tab=workloads&q=${encodeURIComponent(w.name)}&ns=${encodeURIComponent(w.namespace)}`,
            ),
        })),
      },
      {
        id: "pods",
        heading: "Pods",
        items: (cluster?.pods ?? []).map((p) => ({
          id: `pod:${p.namespace}/${p.name}`,
          value: `pod ${p.namespace}/${p.name}`,
          label: `${p.namespace}/${p.name}`,
          icon: Container,
          onSelect: () => ctx.go(`/cluster/pods/${p.namespace}/${p.name}`),
        })),
      },
      {
        id: "apps",
        heading: "Apps",
        items: (apps?.apps ?? []).map((a) => ({
          id: `app:${a.href}`,
          value: `app ${a.name} ${a.href}`,
          label: a.name,
          hint: a.group,
          icon: ExternalLink,
          onSelect: () => {
            ctx.close();
            window.open(a.href, "_blank", "noopener,noreferrer");
          },
        })),
      },
    ];
  },
});
