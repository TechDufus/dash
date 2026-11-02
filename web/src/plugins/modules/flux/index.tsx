import { GitMerge, Zap } from "lucide-react";
import { lazy } from "react";
import type { FluxData } from "@/api/types";
import { definePlugin, type PaletteGroup, topic } from "../../types";
import { reconcileFlux } from "./actions";

const GitOpsPage = lazy(() =>
  import("@/pages/gitops").then((m) => ({ default: () => <m.GitOpsPage /> })),
);

export default definePlugin({
  id: "flux",
  order: 30,
  topics: { "flux.objects": topic<FluxData>() },
  routes: [{ path: "/gitops", page: GitOpsPage }],
  palette: (ctx): readonly PaletteGroup[] => {
    const flux = ctx.topic("flux.objects");
    // Reconcile is a framework action: it is offered while the plugin lists it (actions.enabled)
    // and the role meets its minRole, so the group is empty (and hidden) otherwise. One item per
    // source the plugin verifies (the pins and every source with spec.verify); the kind is in the
    // id because a GitRepository and a Kustomization may share namespace and name.
    const canReconcile = ctx.actionAvailable("flux", "reconcile");
    return [
      {
        id: "actions",
        heading: "Actions",
        items: (canReconcile ? (flux?.verifiedSources ?? []) : []).map((s) => ({
          id: `reconcile:${s.kind}/${s.namespace}/${s.name}`,
          value: `action reconcile ${s.kind} ${s.namespace}/${s.name}`,
          label: `Reconcile ${s.namespace}/${s.name}`,
          hint: s.kind,
          icon: Zap,
          rank: -1, // first in Actions, ahead of the Restart items of the earlier modules
          onSelect: () => {
            ctx.close();
            void reconcileFlux(s.kind, s.namespace, s.name);
          },
        })),
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
