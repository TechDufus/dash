import { GitMerge, Zap } from "lucide-react";
import { lazy } from "react";
import { reconcileFlux } from "@/api/actions";
import type { FluxData } from "@/api/types";
import { definePlugin, type PaletteGroup, topic } from "../../types";

const GitOpsPage = lazy(() =>
  import("@/pages/gitops").then((m) => ({ default: () => <m.GitOpsPage /> })),
);

export default definePlugin({
  id: "flux",
  order: 30,
  topics: { flux: topic<FluxData>() },
  routes: [{ path: "/gitops", page: GitOpsPage }],
  palette: (ctx): readonly PaletteGroup[] => {
    const flux = ctx.topic("flux");
    return [
      {
        id: "actions",
        heading: "Actions",
        items: [
          {
            id: "reconcile:flux-system",
            value: "action reconcile flux-system",
            label: "Reconcile flux-system",
            icon: Zap,
            rank: -1, // first in Actions, ahead of the Restart items of the earlier modules
            onSelect: () => {
              ctx.close();
              void reconcileFlux("GitRepository", "flux-system", "flux-system");
            },
          },
        ],
      },
      {
        id: "flux",
        heading: "Flux",
        items: [...(flux?.kustomizations ?? []), ...(flux?.helmReleases ?? [])].map((f) => ({
          id: `flux:${f.kind}/${f.namespace}/${f.name}`,
          value: `flux ${f.kind} ${f.namespace}/${f.name}`,
          label: f.name,
          hint: f.kind,
          icon: GitMerge,
          onSelect: () => ctx.go(`/gitops?q=${encodeURIComponent(f.name)}`),
        })),
      },
    ];
  },
});
