import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { OverviewData, PluginInfo } from "@/api/types";
import { OverviewPage } from "@/pages/overview";
import { contributesPanel, Slot } from "@/plugins/slots";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { panels } from "./panels";

// The topology diagram measures and animates; these tests are about the tile and the volume line.
vi.mock("@/pages/topology", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  TopologyPanel: () => <div data-testid="topology" />,
}));

// Motion reads prefers-reduced-motion once; with it on a KPI tile prints its value at once.
const matchMedia = window.matchMedia;
window.matchMedia = (query: string) => ({
  ...matchMedia(query),
  matches: query.includes("prefers-reduced-motion"),
});

const row = (state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "kubernetes",
    state,
    topics: ["kubernetes.cluster", "kubernetes.storage", "kubernetes.apps"],
    nav: [{ path: "/cluster", label: "Cluster", title: "Kubernetes cluster", icon: "Boxes" }],
  });

const overview = (kpiList: unknown[]): OverviewData =>
  ({
    status: "ok",
    attention: [],
    kpiList,
    topology: { hosts: [], vms: [], groups: [] },
  }) as unknown as OverviewData;

const kpi = (key: string, value: number, health = "ok", label = key) => ({
  plugin: "kubernetes",
  key,
  label,
  unit: "",
  value,
  health,
});

describe("the nodes tile", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  it("draws one tile from the nodes.ready and nodes.total KPIs, with the ready KPI's health", async () => {
    renderApp(
      <Slot
        id="overview.kpis"
        data={overview([kpi("nodes.ready", 2, "crit"), kpi("nodes.total", 3)])}
      />,
    );
    stream.plugins([row()]);
    const tile = await screen.findByRole("link", { name: "Nodes 2 of 3 ready. Open cluster" });
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(tile).toHaveAttribute("href", "/cluster");
    expect(tile.textContent).toContain("2");
    expect(tile.textContent).toContain("/ 3");
    expect(screen.getByRole("img", { name: "Critical" })).toBeInTheDocument();
  });

  it("does not recompute the health: an ok KPI stays ok though ready < total", async () => {
    renderApp(
      <Slot
        id="overview.kpis"
        data={overview([kpi("nodes.ready", 2, "ok"), kpi("nodes.total", 3)])}
      />,
    );
    stream.plugins([row()]);
    await screen.findByRole("link", { name: "Nodes 2 of 3 ready. Open cluster" });
    expect(screen.getByRole("img", { name: "OK" })).toBeInTheDocument();
  });

  it("draws nothing while either KPI is missing, or when they belong to another plugin", () => {
    const { rerender } = renderApp(
      <Slot id="overview.kpis" data={overview([kpi("nodes.ready", 2)])} />,
    );
    stream.plugins([row()]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
    rerender(<Slot id="overview.kpis" data={overview([kpi("nodes.total", 3)])} />);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
    rerender(
      <Slot
        id="overview.kpis"
        data={overview([
          { ...kpi("nodes.ready", 2), plugin: "other" },
          { ...kpi("nodes.total", 3), plugin: "other" },
        ])}
      />,
    );
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("is hidden with its plugin", () => {
    renderApp(
      <Slot id="overview.kpis" data={overview([kpi("nodes.ready", 3), kpi("nodes.total", 3)])} />,
    );
    stream.plugins([row("disabled")]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("claims the plugin's overview tile", () => {
    expect(contributesPanel("kubernetes", "overview.kpis")).toBe(true);
  });
});

describe("the panels' topics", () => {
  it("reads kubernetes.storage for the storage panels and kubernetes.apps for the apps section", () => {
    for (const p of panels.storage ?? []) expect(p.reads, p.id).toEqual(["kubernetes.storage"]);
    for (const p of panels["apps.sources"] ?? [])
      expect(p.reads, p.id).toEqual(["kubernetes.apps"]);
  });
});

describe("the fullest volume on the overview", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const volume = (health = "crit") => kpi("pvc.worst", 0.91, health, "media/library");

  it("names the volume from the pvc.worst KPI, with its used ratio in the KPI's health", () => {
    renderApp(<OverviewPage />);
    stream.plugins([row()]);
    stream.send("core.overview", overview([volume()]));
    const line = screen.getByRole("link", { name: /Fullest volume: media\/library/ });
    expect(line).toHaveAttribute("href", "/storage");
    expect(line).toHaveTextContent("91%");
    expect(line.querySelector(".text-crit")).not.toBeNull();
  });

  it("is not drawn as a second generic tile", () => {
    renderApp(<OverviewPage />);
    stream.plugins([row()]);
    stream.send(
      "core.overview",
      overview([volume(), kpi("nodes.ready", 3), kpi("nodes.total", 3)]),
    );
    expect(screen.queryByText("media/library", { selector: "[data-slot=panel] *" })).toBeNull();
  });

  it("is absent without the KPI, and with the plugin", () => {
    renderApp(<OverviewPage />);
    stream.plugins([row()]);
    stream.send("core.overview", overview([]));
    expect(screen.queryByText(/Fullest volume/)).toBeNull();

    stream.send("core.overview", overview([volume()]));
    expect(screen.getByText(/Fullest volume/)).toBeInTheDocument();
    stream.plugins([row("disabled")]);
    expect(screen.queryByText(/Fullest volume/)).toBeNull();
  });
});
