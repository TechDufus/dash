import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { OverviewData, PluginInfo } from "@/api/types";
import { contributesPanel, Slot } from "@/plugins/slots";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import type { NodeData, PVEStorageData } from "./model.gen";

// Motion reads prefers-reduced-motion once; with it on a KPI tile prints its value at once.
const matchMedia = window.matchMedia;
window.matchMedia = (query: string) => ({
  ...matchMedia(query),
  matches: query.includes("prefers-reduced-motion"),
});

const row = (state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "proxmox",
    state,
    topics: ["proxmox.vms", "proxmox.node", "proxmox.storage"],
    nav: [{ path: "/vms", label: "VMs", title: "Virtual machines", icon: "Server" }],
  });

const overview = (kpiList: unknown[]): OverviewData =>
  ({
    status: "ok",
    attention: [],
    kpiList,
    topology: { host: {}, vms: [], groups: [] },
    kpis: {},
  }) as unknown as OverviewData;

const kpi = (key: string, value: number, health = "ok") => ({
  plugin: "proxmox",
  key,
  label: key,
  unit: "",
  value,
  health,
});

describe("the VMs tile", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  it("draws one tile from the vms.running and vms.total KPIs, with the running KPI's health", async () => {
    renderApp(
      <Slot
        id="overview.kpis"
        data={overview([kpi("vms.running", 5, "warn"), kpi("vms.total", 6)])}
      />,
    );
    stream.plugins([row()]);
    const tile = await screen.findByRole("link", { name: "VMs 5 of 6 running. Open VMs" });
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(tile).toHaveAttribute("href", "/vms");
    expect(tile.textContent).toContain("5");
    expect(tile.textContent).toContain("/ 6");
    expect(screen.getByRole("img", { name: "Warning" })).toBeInTheDocument();
  });

  it("does not recompute the health: an ok KPI stays ok though running < total", async () => {
    renderApp(
      <Slot
        id="overview.kpis"
        data={overview([kpi("vms.running", 5, "ok"), kpi("vms.total", 6)])}
      />,
    );
    stream.plugins([row()]);
    await screen.findByRole("link", { name: "VMs 5 of 6 running. Open VMs" });
    expect(screen.getByRole("img", { name: "OK" })).toBeInTheDocument();
    expect(screen.queryByRole("img", { name: "Warning" })).toBeNull();
  });

  it("draws nothing while either KPI is missing", () => {
    const { rerender } = renderApp(
      <Slot id="overview.kpis" data={overview([kpi("vms.running", 5, "warn")])} />,
    );
    stream.plugins([row()]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
    rerender(<Slot id="overview.kpis" data={overview([kpi("vms.total", 6)])} />);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("ignores another plugin's KPI of the same key", () => {
    renderApp(
      <Slot
        id="overview.kpis"
        data={overview([
          { ...kpi("vms.running", 5), plugin: "other" },
          { ...kpi("vms.total", 6), plugin: "other" },
        ])}
      />,
    );
    stream.plugins([row()]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("is hidden with its plugin", () => {
    renderApp(
      <Slot id="overview.kpis" data={overview([kpi("vms.running", 5), kpi("vms.total", 6)])} />,
    );
    stream.plugins([row("disabled")]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("claims the plugin's overview tile", () => {
    expect(contributesPanel("proxmox", "overview.kpis")).toBe(true);
  });
});

describe("the Proxmox storage panel", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const storage = (storages: PVEStorageData["storages"]): PVEStorageData => ({ storages });

  it("reads proxmox.storage itself", async () => {
    renderApp(<Slot id="storage" />);
    stream.plugins([row()]);
    stream.send(
      "proxmox.storage",
      storage([
        { ref: "proxmox:Storage/local-zfs", name: "local-zfs", usedBytes: 1, sizeBytes: 2 },
        { ref: "proxmox:Storage/nas", name: "nas", usedBytes: 1, sizeBytes: 4 },
      ]),
    );
    expect(await screen.findByRole("heading", { name: "Proxmox storage" })).toBeInTheDocument();
    expect(screen.getByText("local-zfs")).toBeInTheDocument();
    expect(screen.getByText("nas")).toBeInTheDocument();
  });

  it("says so when there is no storage", async () => {
    renderApp(<Slot id="storage" />);
    stream.plugins([row()]);
    stream.send("proxmox.storage", storage([]));
    expect(await screen.findByText("No storages.")).toBeInTheDocument();
  });

  it("shows its own skeleton until the envelope arrives", () => {
    renderApp(<Slot id="storage" />);
    stream.plugins([row()]);
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(screen.queryByRole("heading", { name: "Proxmox storage" })).toBeNull();
  });

  it("is hidden when proxmox is off", () => {
    renderApp(<Slot id="storage" />);
    stream.plugins([row("disabled")]);
    stream.send("proxmox.storage", storage([]));
    expect(screen.queryByRole("heading", { name: "Proxmox storage" })).toBeNull();
    expect(document.querySelector("[aria-busy=true]")).toBeNull();
  });
});

describe("the Proxmox node facts", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const node = (over: Partial<NodeData> = {}): NodeData => ({
    ref: "proxmox:Node/pve",
    node: "pve",
    version: "9.2.2",
    kernel: "6.17.2-1-pve",
    cpuModel: "Xeon E5-2690",
    uptimeSeconds: 90_000,
    ...over,
  });

  it("draws the four facts from proxmox.node", async () => {
    renderApp(<Slot id="hardware.facts" />);
    stream.plugins([row()]);
    stream.send("proxmox.node", node());
    expect(await screen.findByText("Proxmox VE")).toBeInTheDocument();
    const facts = Object.fromEntries(
      screen.getAllByRole("term").map((t) => [t.textContent, t.nextElementSibling?.textContent]),
    );
    expect(facts).toEqual({
      Uptime: "1d 1h",
      "Proxmox VE": "9.2.2",
      Kernel: "6.17.2-1-pve",
      CPU: "Xeon E5-2690",
    });
  });

  it("shows a dash for a fact the node did not report", async () => {
    renderApp(<Slot id="hardware.facts" />);
    stream.plugins([row()]);
    stream.send("proxmox.node", node({ version: "", kernel: "", cpuModel: "" }));
    await screen.findByText("Proxmox VE");
    expect(screen.getAllByText("–")).toHaveLength(3);
  });

  it("draws nothing before the envelope arrives", () => {
    const { container } = renderApp(<Slot id="hardware.facts" />);
    stream.plugins([row()]);
    expect(container.querySelector("dl")).toBeNull();
    expect(screen.queryByText("Proxmox VE")).toBeNull();
  });

  it("is hidden when proxmox is off", () => {
    const { container } = renderApp(<Slot id="hardware.facts" />);
    stream.plugins([row("disabled")]);
    stream.send("proxmox.node", node());
    expect(container.querySelector("dl")).toBeNull();
    expect(screen.queryByText("Proxmox VE")).toBeNull();
  });

  it("claims the hardware.facts slot", () => {
    expect(contributesPanel("proxmox", "hardware.facts")).toBe(true);
  });
});
