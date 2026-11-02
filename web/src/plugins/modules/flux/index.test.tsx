import { waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { FluxData, FluxObj, FluxSourceRef } from "@/api/types";
import { stubFetch } from "@/test/harness";
import type { PaletteCtx } from "../../types";
import flux from "./index";

const obj = (kind: FluxObj["kind"], namespace: string, name: string): FluxObj => ({
  ref: `flux:${kind}/${namespace}/${name}`,
  kind,
  namespace,
  name,
  ready: true,
  suspended: false,
  suspendReason: "",
  revision: "",
  message: "",
  specVerify: false,
  links: [],
});

// A GitRepository and a Kustomization that share namespace and name, as the standard Flux
// bootstrap creates them, plus a signed OCI source.
const SOURCES: FluxSourceRef[] = [
  { kind: "GitRepository", namespace: "gitops", name: "root" },
  { kind: "Kustomization", namespace: "gitops", name: "root" },
  { kind: "OCIRepository", namespace: "tenant", name: "app" },
];

const data: FluxData = {
  sources: [obj("GitRepository", "gitops", "root")],
  kustomizations: [obj("Kustomization", "gitops", "root")],
  helmReleases: [obj("HelmRelease", "media", "player")],
  verifiedSources: SOURCES,
};

function ctxWith(available: boolean, payload: FluxData | null = data) {
  const calls: string[] = [];
  const close = vi.fn();
  const ctx: PaletteCtx = {
    topic: ((name: string) =>
      name === "flux.objects" ? payload : undefined) as PaletteCtx["topic"],
    go: (to) => calls.push(to),
    close,
    actionAvailable: (id, action) => {
      calls.push(`available:${id}/${action}`);
      return available;
    },
  };
  return { ctx, calls, close };
}

afterEach(() => vi.unstubAllGlobals());

describe("the flux module", () => {
  it("declares the plugin's topic under its plugin-prefixed name", () => {
    expect(Object.keys(flux.topics)).toEqual(["flux.objects"]);
  });

  it("offers one Reconcile item per verified source, with the kind in the id", () => {
    const { ctx, calls } = ctxWith(true);
    const groups = flux.palette?.(ctx) ?? [];
    expect(calls).toContain("available:flux/reconcile");
    const items = groups.find((g) => g.id === "actions")?.items ?? [];
    expect(items.map((i) => i.id)).toEqual([
      "reconcile:GitRepository/gitops/root",
      "reconcile:Kustomization/gitops/root",
      "reconcile:OCIRepository/tenant/app",
    ]);
    expect(new Set(items.map((i) => i.id)).size).toBe(items.length);
    expect(new Set(items.map((i) => i.value)).size).toBe(items.length);
    expect(items.map((i) => [i.label, i.hint])).toEqual([
      ["Reconcile gitops/root", "GitRepository"],
      ["Reconcile gitops/root", "Kustomization"],
      ["Reconcile tenant/app", "OCIRepository"],
    ]);
    expect(items.every((i) => i.rank === -1)).toBe(true);
  });

  it("offers no Reconcile item when the action is not available, and keeps the Flux group", () => {
    const { ctx } = ctxWith(false);
    const groups = flux.palette?.(ctx) ?? [];
    expect(groups.find((g) => g.id === "actions")?.items).toEqual([]);
    const items = groups.find((g) => g.id === "flux")?.items ?? [];
    expect(items.map((i) => i.id)).toEqual([
      "flux:Kustomization/gitops/root",
      "flux:HelmRelease/media/player",
    ]);
  });

  it("offers nothing before the topic arrives, and nothing for a payload that verifies no source", () => {
    expect(flux.palette?.(ctxWith(true, null).ctx).flatMap((g) => g.items)).toEqual([]);
    const none = ctxWith(true, { ...data, verifiedSources: [] });
    expect(flux.palette?.(none.ctx).find((g) => g.id === "actions")?.items).toEqual([]);
  });

  it("filters the page by the object's name when a Flux item is chosen", () => {
    const { ctx, calls } = ctxWith(true);
    const items = flux.palette?.(ctx).find((g) => g.id === "flux")?.items ?? [];
    items[1]?.onSelect();
    expect(calls.at(-1)).toBe("/gitops?q=player");
  });

  it("closes the palette and posts the chosen source to the plugin's action", async () => {
    const stub = stubFetch({
      "POST /api/v1/plugins/flux/actions/reconcile": { status: "accepted" },
    });
    const { ctx, close } = ctxWith(true);
    const items = flux.palette?.(ctx).find((g) => g.id === "actions")?.items ?? [];
    items[2]?.onSelect();
    expect(close).toHaveBeenCalledOnce();
    await waitFor(() =>
      expect(stub.callsTo("POST /api/v1/plugins/flux/actions/reconcile")).toHaveLength(1),
    );
    expect(stub.callsTo("POST /api/v1/plugins/flux/actions/reconcile")[0]?.body).toEqual({
      kind: "OCIRepository",
      namespace: "tenant",
      name: "app",
    });
    expect(stub.calls.filter((c) => c.path === "/api/actions/reconcile")).toEqual([]);
  });
});
