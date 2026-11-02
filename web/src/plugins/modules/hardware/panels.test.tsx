import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { OverviewData, PluginInfo } from "@/api/types";
import { contributesPanel, Slot } from "@/plugins/slots";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";

// Motion reads prefers-reduced-motion once; with it on a KPI tile prints its value at once.
const matchMedia = window.matchMedia;
window.matchMedia = (query: string) => ({
  ...matchMedia(query),
  matches: query.includes("prefers-reduced-motion"),
});

const row = (state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "hardware",
    state,
    topics: ["hardware.hosts", "hardware.machines"],
    nav: [{ path: "/hardware", label: "Hardware", title: "Hardware", icon: "Cpu" }],
  });

const overview = (kpiList: unknown[]): OverviewData =>
  ({
    status: "ok",
    attention: [],
    kpiList,
    topology: { host: {}, vms: [], groups: [] },
  }) as unknown as OverviewData;

const kpi = (key: string, value: number, health = "ok", plugin = "hardware") => ({
  plugin,
  key,
  label: key,
  unit: "",
  value,
  health,
});

describe("the hardware KPI tiles", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  it("draw the power and CPU temperature KPIs the plugin publishes", async () => {
    renderApp(<Slot id="overview.kpis" data={overview([kpi("power", 240), kpi("cpuTemp", 62)])} />);
    stream.plugins([row()]);
    const power = await screen.findByRole("link", { name: "Power 240 W. Open hardware" });
    const temp = screen.getByRole("link", { name: "CPU temperature 62°C. Open hardware" });
    expect(power).toHaveAttribute("href", "/hardware");
    expect(temp).toHaveAttribute("href", "/hardware");
    expect(power.textContent).toContain("240");
    expect(temp.textContent).toContain("62");
  });

  it("take the CPU temperature's health from the KPI, not from a threshold of their own", async () => {
    // 62 °C would be ok against any fixed threshold; the plugin says warn.
    renderApp(
      <Slot id="overview.kpis" data={overview([kpi("power", 240), kpi("cpuTemp", 62, "warn")])} />,
    );
    stream.plugins([row()]);
    const temp = await screen.findByRole("link", { name: "CPU temperature 62°C. Open hardware" });
    expect(temp.querySelector('[role="img"][aria-label="Warning"]')).not.toBeNull();
    const power = screen.getByRole("link", { name: "Power 240 W. Open hardware" });
    expect(power.querySelector('[role="img"][aria-label="Warning"]')).toBeNull();
  });

  it("draw nothing until the host has polled and the KPIs exist", () => {
    const { container } = renderApp(<Slot id="overview.kpis" data={overview([])} />);
    stream.plugins([row()]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
    expect(container.querySelector('[data-slot="panel"]')).toBeNull();
  });

  it("ignore a KPI of the same key from another plugin", () => {
    renderApp(
      <Slot
        id="overview.kpis"
        data={overview([kpi("power", 5, "ok", "other"), kpi("cpuTemp", 5, "ok", "other")])}
      />,
    );
    stream.plugins([row()]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("draw only the tile whose KPI exists", async () => {
    renderApp(<Slot id="overview.kpis" data={overview([kpi("power", 100)])} />);
    stream.plugins([row()]);
    await screen.findByRole("link", { name: "Power 100 W. Open hardware" });
    expect(screen.getAllByRole("link")).toHaveLength(1);
  });

  it("are hidden when the hardware plugin is off", () => {
    renderApp(<Slot id="overview.kpis" data={overview([kpi("power", 240), kpi("cpuTemp", 62)])} />);
    stream.plugins([row("disabled")]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("claim the overview.kpis slot", () => {
    expect(contributesPanel("hardware", "overview.kpis")).toBe(true);
  });
});
