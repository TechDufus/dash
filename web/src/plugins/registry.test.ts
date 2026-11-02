import { describe, expect, it } from "vitest";
import { CORE_PAGES, MODULES, moduleById, moduleOrder } from "./registry";

describe("module registry", () => {
  it("lists eleven modules with unique ids", () => {
    const ids = MODULES.map((m) => m.id);
    expect(ids).toHaveLength(11);
    expect(new Set(ids).size).toBe(ids.length);
    expect(moduleById.size).toBe(ids.length);
  });

  it("declares no topic twice (the type-level merge would silently intersect)", () => {
    const seen = new Map<string, string>();
    for (const m of MODULES) {
      for (const name of Object.keys(m.topics)) {
        expect(
          seen.get(name),
          `${name} is declared by ${seen.get(name)} and ${m.id}`,
        ).toBeUndefined();
        seen.set(name, m.id);
      }
    }
    expect([...seen.keys()].sort()).toEqual([
      "alertmanager.alerts",
      "cnpg.clusters",
      "flux.objects",
      "gatus.status",
      "hardware.hosts",
      "hardware.machines",
      "kubernetes.apps",
      "kubernetes.cluster",
      "kubernetes.storage",
      "launcher.links",
      "proxmox.node",
      "proxmox.storage",
      "proxmox.vms",
      "rustfs.usage",
    ]);
  });

  it("gives every route pattern to exactly one module", () => {
    const patterns = MODULES.flatMap((m) => (m.routes ?? []).map((r) => r.path));
    expect(patterns.sort()).toEqual(
      ["/alerts", "/cluster/*?", "/gitops", "/hardware", "/status", "/vms/:vmid?"].sort(),
    );
    expect(new Set(patterns).size).toBe(patterns.length);
  });

  it("orders modules and core pages into today's rail", () => {
    const ranked = [
      ...CORE_PAGES.map((c) => ({ key: c.key, order: c.order })),
      ...MODULES.filter((m) => m.routes?.length).map((m) => ({ key: m.id, order: moduleOrder(m) })),
    ].sort((a, b) => a.order - b.order);
    expect(ranked.map((r) => r.key)).toEqual([
      "core:overview",
      "proxmox",
      "kubernetes",
      "flux",
      "hardware",
      "core:storage",
      "alertmanager",
      "gatus",
      "core:apps",
    ]);
  });

  it("puts a module without an order after every numbered entry, in registry order", () => {
    const numbered = [...CORE_PAGES.map((c) => c.order), ...MODULES.flatMap((m) => m.order ?? [])];
    const grafana = moduleById.get("grafana");
    const headlamp = moduleById.get("headlamp");
    if (!grafana || !headlamp) throw new Error("module missing");
    expect(moduleOrder(grafana)).toBeGreaterThan(Math.max(...numbered));
    expect(moduleOrder(headlamp)).toBeGreaterThan(moduleOrder(grafana));
  });
});
