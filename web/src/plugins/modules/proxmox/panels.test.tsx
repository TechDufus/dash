import { screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { HostData, OverviewData, PluginInfo } from "@/api/types";
import { contributesPanel, Slot } from "@/plugins/slots";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import type { NodeData, PVEClusterData, PVEStorage, PVEStorageData } from "./model.gen";

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
    topics: ["proxmox.vms", "proxmox.cluster", "proxmox.storage"],
    nav: [{ path: "/vms", label: "VMs", title: "Virtual machines", icon: "Server" }],
  });

const overview = (kpiList: unknown[]): OverviewData =>
  ({
    status: "ok",
    attention: [],
    kpiList,
    topology: { hosts: [], vms: [], groups: [] },
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

  const storage = (storages: PVEStorage[], usedBytes = 0, sizeBytes = 0): PVEStorageData => ({
    storages,
    usedBytes,
    sizeBytes,
  });

  it("labels each storage with its node or as shared, and shows the totals", async () => {
    renderApp(<Slot id="storage" />);
    stream.plugins([row()]);
    stream.send(
      "proxmox.storage",
      storage(
        [
          {
            ref: "proxmox:Storage/nas",
            name: "nas",
            node: "",
            shared: true,
            usedBytes: 1024,
            sizeBytes: 4096,
          },
          {
            ref: "proxmox:Storage/pve-1/local-zfs",
            name: "local-zfs",
            node: "pve-1",
            shared: false,
            usedBytes: 1024,
            sizeBytes: 2048,
          },
          {
            ref: "proxmox:Storage/pve-2/local-zfs",
            name: "local-zfs",
            node: "pve-2",
            shared: false,
            usedBytes: 0,
            sizeBytes: 2048,
          },
        ],
        2048,
        8192,
      ),
    );
    expect(await screen.findByRole("heading", { name: "Proxmox storage" })).toBeInTheDocument();
    const bars = screen.getAllByRole("progressbar").map((b) => b.getAttribute("aria-label"));
    expect(bars).toEqual(["Total", "nas · shared", "local-zfs · pve-1", "local-zfs · pve-2"]);
    // The totals count the shared storage once, as the payload does.
    expect(screen.getByText("2.0 KiB / 8.0 KiB")).toBeInTheDocument();
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
    ref: "proxmox:Node/pve-1",
    node: "pve-1",
    status: "online",
    cpuRatio: 0.1,
    threads: 8,
    memUsedBytes: 1,
    memTotalBytes: 2,
    rootUsedBytes: 1,
    rootTotalBytes: 2,
    uptimeSeconds: 90_000,
    version: "9.2.2",
    kernel: "6.17.2-1-pve",
    cpuModel: "Example CPU 2.4GHz",
    cpuSockets: 1,
    cpuCores: 4,
    ...over,
  });

  const cluster = (nodes: NodeData[]): PVEClusterData => ({
    ref: "proxmox:Cluster/lab",
    name: "lab",
    standalone: false,
    quorumKnown: true,
    quorate: true,
    nodes,
    totals: {
      nodes: 0,
      nodesOnline: 0,
      threads: 0,
      cpuRatio: 0,
      memUsedBytes: 0,
      memTotalBytes: 0,
    },
  });

  const host = (nodeName: string) => ({ name: "Rack server", node: nodeName }) as HostData;

  it("draws the four facts of the node the host is", async () => {
    renderApp(<Slot id="hardware.facts" host={host("pve-2")} />);
    stream.plugins([row()]);
    stream.send(
      "proxmox.cluster",
      cluster([node(), node({ ref: "proxmox:Node/pve-2", node: "pve-2", version: "9.1.0" })]),
    );
    expect(await screen.findByText("Proxmox VE")).toBeInTheDocument();
    const facts = Object.fromEntries(
      screen.getAllByRole("term").map((t) => [t.textContent, t.nextElementSibling?.textContent]),
    );
    expect(facts).toEqual({
      Uptime: "1d 1h",
      "Proxmox VE": "9.1.0",
      Kernel: "6.17.2-1-pve",
      CPU: "Example CPU 2.4GHz",
    });
  });

  it("draws nothing for a host that is no node of the cluster", async () => {
    renderApp(
      <>
        <section aria-label="pve-1">
          <Slot id="hardware.facts" host={host("pve-1")} />
        </section>
        <section aria-label="pve-9">
          <Slot id="hardware.facts" host={host("pve-9")} />
        </section>
      </>,
    );
    stream.plugins([row()]);
    stream.send("proxmox.cluster", cluster([node()]));
    // The matching host draws its facts, so the envelope has landed for both.
    const known = await screen.findByRole("region", { name: "pve-1" });
    expect(await within(known).findByText("Proxmox VE")).toBeInTheDocument();
    const unknown = screen.getByRole("region", { name: "pve-9" });
    expect(unknown.querySelector("dl")).toBeNull();
  });

  it("shows a dash for a fact the node did not report", async () => {
    renderApp(<Slot id="hardware.facts" host={host("pve-1")} />);
    stream.plugins([row()]);
    stream.send("proxmox.cluster", cluster([node({ version: "", kernel: "", cpuModel: "" })]));
    await screen.findByText("Proxmox VE");
    expect(screen.getAllByText("–")).toHaveLength(3);
  });

  it("names the state of a node that is not online in place of its uptime", async () => {
    renderApp(
      <>
        <section aria-label="pve-1">
          <Slot id="hardware.facts" host={host("pve-1")} />
        </section>
        <section aria-label="pve-2">
          <Slot id="hardware.facts" host={host("pve-2")} />
        </section>
      </>,
    );
    stream.plugins([row()]);
    stream.send(
      "proxmox.cluster",
      cluster([
        node({ status: "offline" }),
        node({ ref: "proxmox:Node/pve-2", node: "pve-2", status: "unknown" }),
      ]),
    );
    const uptime = async (region: string) => {
      const section = await screen.findByRole("region", { name: region });
      const term = await within(section).findByText("Uptime");
      return term.nextElementSibling?.textContent;
    };
    expect(await uptime("pve-1")).toBe("offline");
    expect(await uptime("pve-2")).toBe("unknown");
  });

  it("draws nothing before the envelope arrives", () => {
    const { container } = renderApp(<Slot id="hardware.facts" host={host("pve-1")} />);
    stream.plugins([row()]);
    expect(container.querySelector("dl")).toBeNull();
    expect(screen.queryByText("Proxmox VE")).toBeNull();
  });

  it("is hidden when proxmox is off", () => {
    const { container } = renderApp(<Slot id="hardware.facts" host={host("pve-1")} />);
    stream.plugins([row("disabled")]);
    stream.send("proxmox.cluster", cluster([node()]));
    expect(container.querySelector("dl")).toBeNull();
    expect(screen.queryByText("Proxmox VE")).toBeNull();
  });

  it("claims the hardware.facts slot", () => {
    expect(contributesPanel("proxmox", "hardware.facts")).toBe(true);
  });
});
