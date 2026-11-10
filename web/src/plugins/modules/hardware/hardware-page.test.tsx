import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { Route } from "wouter";
import type { Disk, Fan, HostData, PluginInfo } from "@/api/types";
import { formatDuration } from "@/lib/format";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { type FetchStub, pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { fanScale, HardwarePage, MAX_BAYS } from "./hardware-page";
import type { Machine, MachineStats } from "./model.gen";

// Motion reads prefers-reduced-motion once; with it on the page prints its numbers at once.
const matchMedia = window.matchMedia;
window.matchMedia = (query: string) => ({
  ...matchMedia(query),
  matches: query.includes("prefers-reduced-motion"),
});

const hardware = (state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "hardware",
    state,
    topics: ["hardware.hosts", "hardware.machines"],
    nav: [{ path: "/hardware", label: "Hardware", title: "Hardware", icon: "Cpu" }],
  });

const proxmox = (): PluginInfo =>
  pluginInfo({ id: "proxmox", topics: ["proxmox.vms", "proxmox.cluster", "proxmox.storage"] });

const host = (over: Partial<HostData> = {}): HostData => ({
  model: "",
  ref: "hardware:Host/pve-1",
  node: "pve-1",
  bays: 0,
  fanMaxRpm: 0,
  sensorAliases: { inlet: "inlet", exhaust: "exhaust" },
  tempWarnC: 85,
  tempCritC: 90,
  cpuTempHealth: "ok",
  health: "ok",
  name: "pve-1",
  cpuRatio: 0.1,
  memUsedBytes: 1,
  memTotalBytes: 2,
  powerWatts: 200,
  packageTempsC: [],
  sensors: [],
  fans: [],
  psus: [],
  pools: [],
  disks: [],
  selFreeBytes: 0,
  scrubTracked: false,
  links: [],
  ...over,
});

const disk = (bay: number, over: Partial<Disk> = {}): Disk => ({
  ref: `hardware:Disk/pve-1/S${bay}`,
  bay: String(bay),
  serial: `S${bay}`,
  device: `/dev/sd${bay}`,
  healthy: true,
  tempC: 30,
  ...over,
});

const fan = (name: string, rpm: number): Fan => ({ name, rpm });

const machine = (
  name: string,
  up: boolean,
  source = "prometheus",
  stats?: MachineStats,
): Machine => ({
  ref: `hardware:Machine/${name}`,
  name,
  up,
  source,
  ...(stats ? { stats } : {}),
});

const linuxStats = (over: Partial<MachineStats> = {}): MachineStats => ({
  os: "Linux",
  arch: "x86_64",
  kernel: "6.8.0",
  cpus: 8,
  cpuRatio: 0.25,
  memUsedBytes: 4 * 2 ** 30,
  memTotalBytes: 16 * 2 ** 30,
  diskUsedBytes: 100 * 2 ** 30,
  diskTotalBytes: 500 * 2 ** 30,
  load1: 0.5,
  load5: 0.75,
  load15: 1,
  uptimeSeconds: 90_000,
  tempC: 54,
  ...over,
});

const darwinStats = (): MachineStats => {
  const { tempC: _none, ...rest } = linuxStats({ os: "Darwin", arch: "arm64", kernel: "24.0.0" });
  return rest;
};

/** The tile of a temperature label ("Inlet", "Exhaust", "CPU package"). */
const tile = (label: string) => {
  const el = screen.getByText(label).closest('[data-slot="panel"]');
  if (!el) throw new Error(`no tile ${label}`);
  return within(el as HTMLElement);
};

/** The bay cells of the grid: drawn disks and empty slots. */
const bayCells = () => [
  ...screen.queryAllByRole("button", { name: /^Bay / }),
  ...screen.queryAllByText("empty"),
];

/** How full a gauge is, read back from its arc (the filled arc is 0.75 * circumference * ratio). */
const gaugeFill = (name: RegExp) => {
  const svg = screen.getByRole("img", { name });
  const arc = svg.querySelectorAll("circle")[1];
  const [dash, circ] = (arc?.getAttribute("stroke-dasharray") ?? "").split(" ").map(Number);
  if (dash === undefined || circ === undefined) throw new Error("gauge has no arc");
  return dash / (0.75 * circ);
};

describe("the Hardware page", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const draw = (data: HostData) => {
    renderApp(<HardwarePage />);
    stream.plugins([hardware(), proxmox()]);
    stream.send("hardware.hosts", { hosts: [data] });
  };

  /** The page behind its module route, at `path`, so links and the route's host take effect. */
  const drawAt = (path: string, hosts: HostData[], machines: Machine[] = []) => {
    const r = renderApp(
      <Route path="/hardware/:host?">{(p) => <HardwarePage host={p.host} />}</Route>,
      { path },
    );
    stream.plugins([hardware(), proxmox()]);
    stream.send("hardware.hosts", { hosts });
    stream.send("hardware.machines", { machines });
    return r;
  };

  /** The `node` of every series request, by series name. */
  const seriesNodes = (stub: FetchStub) =>
    stub.calls
      .filter((c) => c.path === "/api/v1/series")
      .map((c) => `${c.query.get("name")} ${c.query.get("node")}`);

  describe("the title", () => {
    it("shows the model the host carries next to the host's name", () => {
      draw(host({ model: "Example 2U server" }));
      expect(screen.getByText("pve-1 · Example 2U server")).toBeInTheDocument();
    });

    it("shows only the name when no model is configured", () => {
      draw(host());
      expect(screen.getByText("pve-1")).toBeInTheDocument();
      expect(screen.queryByText(/pve-1 ·/)).toBeNull();
    });
  });

  describe("the bay grid", () => {
    it("draws the configured number of slots, empty ones included", () => {
      draw(host({ bays: 16, disks: [disk(1), disk(2)] }));
      expect(bayCells()).toHaveLength(16);
      expect(screen.getAllByRole("button", { name: /^Bay / })).toHaveLength(2);
      expect(screen.getByRole("button", { name: "Bay 1: healthy" })).toBeInTheDocument();
    });

    it("derives the grid from the highest populated slot when bays is 0", () => {
      draw(host({ bays: 0, disks: [disk(0), disk(1), disk(5)] }));
      expect(bayCells()).toHaveLength(6);
      expect(screen.getByRole("button", { name: "Bay 5: healthy" })).toBeInTheDocument();
    });

    it("derives a grid numbered from 1 when no disk sits in slot 0", () => {
      draw(host({ bays: 0, disks: [disk(1), disk(2), disk(4)] }));
      expect(bayCells()).toHaveLength(4);
      expect(screen.getByRole("button", { name: "Bay 4: healthy" })).toBeInTheDocument();
      expect(screen.getByText("#3")).toBeInTheDocument(); // the empty slot between them
    });

    it("lists the disks sorted when the derived grid would be absurdly large", () => {
      // 99999999999 used to throw RangeError: Invalid array length (the error screen).
      draw(host({ bays: 0, disks: [disk(99999999999)] }));
      expect(bayCells()).toHaveLength(1);
      expect(screen.getByRole("button", { name: "Bay 99999999999: healthy" })).toBeInTheDocument();
    });

    it("lists the disks sorted when the highest slot is far above the others", () => {
      // 20000 used to draw 20001 slots.
      draw(host({ bays: 0, disks: [disk(20000), disk(1)] }));
      expect(bayCells()).toHaveLength(2);
      const names = screen
        .getAllByRole("button", { name: /^Bay / })
        .map((b) => b.getAttribute("aria-label"));
      expect(names).toEqual(["Bay 1: healthy", "Bay 20000: healthy"]);
    });

    it("derives a grid of MAX_BAYS slots", () => {
      draw(host({ bays: 0, disks: [disk(1), disk(MAX_BAYS)] }));
      expect(bayCells()).toHaveLength(MAX_BAYS);
    });

    it("lists the disks sorted one slot above MAX_BAYS", () => {
      draw(host({ bays: 0, disks: [disk(1), disk(MAX_BAYS + 1)] }));
      expect(bayCells()).toHaveLength(2);
    });

    it("keeps every disk when a bay number does not fit the configured grid", () => {
      draw(host({ bays: 4, disks: [disk(1), disk(9)] }));
      expect(screen.getAllByRole("button", { name: /^Bay / })).toHaveLength(2);
      expect(bayCells()).toHaveLength(4);
    });

    it("says so when there are no disks and no grid size", () => {
      draw(host({ bays: 0, disks: [] }));
      expect(screen.getByText("No disks reported.")).toBeInTheDocument();
      expect(bayCells()).toHaveLength(0);
    });

    it("draws an all-empty grid of the configured size when there are no disks", () => {
      draw(host({ bays: 8, disks: [] }));
      expect(screen.getAllByText("empty")).toHaveLength(8);
    });
  });

  describe("the fan gauges", () => {
    it("scales to the configured maximum while the fans run below it", () => {
      draw(host({ fanMaxRpm: 18000, fans: [fan("Fan 1", 9000), fan("Fan 2", 18000)] }));
      expect(gaugeFill(/^Fan 1 /)).toBeCloseTo(0.5, 5);
      expect(gaugeFill(/^Fan 2 /)).toBeCloseTo(1, 5);
    });

    it("scales to the fastest fan when one runs above the configured maximum", () => {
      draw(host({ fanMaxRpm: 18000, fans: [fan("Fan 1", 9000), fan("Fan 2", 24000)] }));
      expect(gaugeFill(/^Fan 1 /)).toBeCloseTo(9000 / 24000, 5);
      expect(gaugeFill(/^Fan 2 /)).toBeCloseTo(1, 5);
    });

    it("scales to the fastest fan when no maximum is configured", () => {
      draw(host({ fanMaxRpm: 0, fans: [fan("Fan 1", 3000), fan("Fan 2", 12000)] }));
      expect(gaugeFill(/^Fan 1 /)).toBeCloseTo(0.25, 5);
      expect(gaugeFill(/^Fan 2 /)).toBeCloseTo(1, 5);
    });

    it("draws empty gauges and flags the fans when everything is stopped and no maximum is set", () => {
      draw(host({ fanMaxRpm: 0, fans: [fan("Fan 1", 0)] }));
      expect(gaugeFill(/^Fan 1 /)).toBe(0);
      expect(screen.getByText("Stopped")).toBeInTheDocument();
    });

    it("computes the scale as the larger of the maximum and the fastest fan", () => {
      expect(fanScale(18000, [])).toBe(18000);
      expect(fanScale(0, [])).toBe(0);
      expect(fanScale(18000, [20000, 5])).toBe(20000);
    });
  });

  describe("the Inlet and Exhaust tiles", () => {
    const sensors = [
      { name: "Inlet Temp", valueC: 18 },
      { name: "Exhaust Temp", valueC: 33 },
      { name: "Ambient", valueC: 21 },
      { name: "Outlet", valueC: 40 },
    ];

    it("read the sensors the default aliases name", () => {
      draw(host({ sensors }));
      expect(tile("Inlet").getByText("18°C")).toBeInTheDocument();
      expect(tile("Exhaust").getByText("33°C")).toBeInTheDocument();
    });

    it("read the sensors the configured aliases name, ignoring case", () => {
      draw(host({ sensors, sensorAliases: { inlet: "AMBIENT", exhaust: "outlet" } }));
      expect(tile("Inlet").getByText("21°C")).toBeInTheDocument();
      expect(tile("Exhaust").getByText("40°C")).toBeInTheDocument();
    });

    it("show a dash, never an arbitrary sensor, when no sensor matches or the alias is empty", () => {
      draw(host({ sensors, sensorAliases: { inlet: "nothing", exhaust: "" } }));
      expect(tile("Inlet").getByText("–")).toBeInTheDocument();
      expect(tile("Exhaust").getByText("–")).toBeInTheDocument();
    });
  });

  describe("the CPU package tile", () => {
    it("shows the health the server computed, not one of its own", () => {
      // 70 °C is fine against the built-in 85/90, but the plugin says crit (thresholds 60/65).
      draw(
        host({
          packageTempsC: [70],
          cpuTempHealth: "crit",
          tempWarnC: 60,
          tempCritC: 65,
        }),
      );
      const cpu = tile("CPU package");
      expect(cpu.getByText("70°C")).toBeInTheDocument();
      expect(cpu.getByText("Critical")).toBeInTheDocument();
      expect(cpu.getByText("warn 60°C · crit 65°C")).toBeInTheDocument();
    });

    it("does not warn about a temperature the server calls ok, however high", () => {
      draw(host({ packageTempsC: [88], cpuTempHealth: "ok", tempWarnC: 95, tempCritC: 100 }));
      const cpu = tile("CPU package");
      expect(cpu.queryByText("Critical")).toBeNull();
      expect(cpu.queryByText("Warning")).toBeNull();
    });

    it("reports no package sensors with an unknown health", () => {
      draw(host({ packageTempsC: [], cpuTempHealth: "ok" }));
      const cpu = tile("CPU package");
      expect(cpu.getByText("No package sensors")).toBeInTheDocument();
      expect(cpu.getByRole("img", { name: "Unknown" })).toBeInTheDocument();
    });
  });

  it("draws the facts of the host's Proxmox node through the hardware.facts slot", async () => {
    draw(host());
    stream.send("proxmox.cluster", {
      nodes: [
        {
          node: "pve-1",
          status: "online",
          version: "8.4.1",
          kernel: "6.8.12-1-pve",
          cpuModel: "Example CPU",
          uptimeSeconds: 3600,
        },
        {
          node: "pve-2",
          status: "online",
          version: "9.2.2",
          kernel: "6.14.8-2-pve",
          cpuModel: "Example CPU",
          uptimeSeconds: 3600,
        },
      ],
    });
    expect(await screen.findByText("Proxmox VE")).toBeInTheDocument();
    expect(screen.getByText("8.4.1")).toBeInTheDocument();
    expect(screen.queryByText("9.2.2")).toBeNull();
  });

  it("uses vendor-neutral words for the management controller's log", () => {
    draw(host({ selFreeBytes: 1024 }));
    expect(screen.getByRole("heading", { name: "System Event Log" })).toBeInTheDocument();
    expect(screen.queryByText(/iDRAC/)).toBeNull();
  });

  describe("with one host", () => {
    it("draws the host's detail alone when there are no machines, with no fleet grid", () => {
      drawAt("/hardware", [host({ model: "Example 2U server" })]);
      expect(screen.getByText("pve-1 · Example 2U server")).toBeInTheDocument();
      expect(screen.getByRole("heading", { name: "Disk bays" })).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Fleet" })).toBeNull();
      expect(screen.queryByRole("link", { name: /^Host / })).toBeNull();
    });

    it("asks for the host's series by its node", () => {
      const stub = stubFetch();
      drawAt("/hardware", [host({ name: "Rack 1", node: "pve-1" })]);
      expect(seriesNodes(stub)).toEqual(
        expect.arrayContaining(["host.power pve-1", "host.cpuTemp pve-1", "host.temps pve-1"]),
      );
    });
  });

  describe("with several hosts", () => {
    // The first host's readings look fine, but the plugin says warn (an attention item the page
    // does not draw): the card shows the plugin's health, not one of its own.
    const two = () => [
      host({ model: "Example 2U server", powerWatts: 240, health: "warn" }),
      host({
        ref: "hardware:Host/Rack 2",
        name: "Rack 2",
        node: "pve-2",
        powerWatts: 120,
        packageTempsC: [92],
        cpuTempHealth: "crit",
        health: "crit",
      }),
    ];

    it("draws a card per host, the first one marked and detailed when the route names none", () => {
      drawAt("/hardware", two());
      expect(screen.getByRole("heading", { name: "Fleet" })).toBeInTheDocument();
      const first = screen.getByRole("link", { name: "Host pve-1, Warning, shown below" });
      expect(first).toHaveAttribute("aria-current", "true");
      expect(first).toHaveAttribute("href", "/hardware/pve-1");
      expect(within(first).getByText("240 W")).toBeInTheDocument();
      const second = screen.getByRole("link", { name: "Host Rack 2, Critical" });
      expect(second).not.toHaveAttribute("aria-current");
      expect(second).toHaveAttribute("href", "/hardware/Rack%202");
      expect(within(second).getByText("92°C")).toHaveClass("text-crit");
      expect(screen.getByText("pve-1 · Example 2U server")).toBeInTheDocument();
    });

    it("details the host the route names, and follows a click on another host's card", async () => {
      const user = userEvent.setup();
      const r = drawAt("/hardware/Rack%202", two());
      expect(screen.getByRole("heading", { name: "Rack 2" })).toBeInTheDocument();
      expect(screen.getByRole("link", { name: /^Host Rack 2, .*shown below/ })).toHaveAttribute(
        "aria-current",
        "true",
      );
      expect(screen.queryByText("pve-1 · Example 2U server")).toBeNull();

      await user.click(screen.getByRole("link", { name: /^Host pve-1/ }));
      expect(r.history.at(-1)).toBe("/hardware/pve-1");
      expect(await screen.findByText("pve-1 · Example 2U server")).toBeInTheDocument();
      expect(screen.getByRole("link", { name: /^Host pve-1, .*shown below/ })).toBeInTheDocument();
    });

    it("asks for the shown host's series by its node, never without one", () => {
      const stub = stubFetch();
      drawAt("/hardware/Rack%202", two());
      const nodes = seriesNodes(stub);
      expect(nodes).toEqual(
        expect.arrayContaining(["host.power pve-2", "host.cpuTemp pve-2", "host.temps pve-2"]),
      );
      expect(nodes.filter((n) => !n.endsWith(" pve-2"))).toEqual([]);
    });

    describe("a host whose name has reserved characters", () => {
      const odd = () => [
        host(),
        host({ ref: "hardware:Host/Rack A, row 1/2", name: "Rack A, row 1/2", node: "pve-2" }),
      ];

      it("is reached through its own card link", async () => {
        const user = userEvent.setup();
        const r = drawAt("/hardware", odd());
        await user.click(screen.getByRole("link", { name: /^Host Rack A, row 1\/2,/ }));
        expect(r.history.at(-1)).toBe("/hardware/Rack%20A%2C%20row%201%2F2");
        expect(await screen.findByRole("heading", { name: "Rack A, row 1/2" })).toBeInTheDocument();
        expect(screen.queryByText(/No host named/)).toBeNull();
      });

      it("is reached through the route the plugin's attention items carry", () => {
        // Go's url.PathEscape: spaces as %20, and "," and "/" escaped as in a path segment.
        drawAt("/hardware/Rack%20A%2C%20row%201%2F2", odd());
        expect(screen.getByRole("heading", { name: "Rack A, row 1/2" })).toBeInTheDocument();
        expect(screen.queryByText(/No host named/)).toBeNull();
      });
    });

    it("asks for a host's series by its node in lowercase, as the API requires", () => {
      const stub = stubFetch();
      drawAt("/hardware", [host({ node: "PVE-2" })]);
      const nodes = seriesNodes(stub);
      expect(nodes).toEqual(expect.arrayContaining(["host.power pve-2", "host.cpuTemp pve-2"]));
      expect(nodes.filter((n) => n.includes("PVE"))).toEqual([]);
    });

    it("says so for a host the route names that does not exist, with a way back", () => {
      drawAt("/hardware/nope", two());
      expect(screen.getByText("nope")).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Back to all hardware" })).toHaveAttribute(
        "href",
        "/hardware",
      );
      expect(screen.queryByRole("heading", { name: "Disk bays" })).toBeNull();
      expect(screen.getAllByRole("link", { name: /^Host / })).toHaveLength(2);
      expect(screen.queryByRole("link", { name: /shown below/ })).toBeNull();
    });
  });

  describe("a host's card", () => {
    const pool = (name: string, state = "ONLINE") => ({
      ref: `hardware:Pool/pve-2/${name}`,
      name,
      state,
      scrubErrors: 0,
      scrubInProgress: false,
    });
    /** The card of the second host (the first is detailed below, so its texts repeat there). */
    const drawCard = (over: Partial<HostData>) => {
      drawAt("/hardware", [
        host(),
        host({ ref: "hardware:Host/pve-2", name: "pve-2", node: "pve-2", ...over }),
      ]);
      return within(screen.getByRole("link", { name: /^Host pve-2,/ }));
    };

    it("shows the CPU as a gauge and the memory as a bar", () => {
      const c = drawCard({ cpuRatio: 0.4, memUsedBytes: 4 * 2 ** 30, memTotalBytes: 16 * 2 ** 30 });
      expect(c.getByRole("img", { name: "CPU 40%" })).toBeInTheDocument();
      expect(c.getByText("Memory")).toBeInTheDocument();
    });

    it("leaves out the memory bar when the host reports no memory", () => {
      const c = drawCard({ memUsedBytes: 0, memTotalBytes: 0 });
      expect(c.getByRole("img", { name: /^CPU / })).toBeInTheDocument();
      expect(c.queryByText("Memory")).toBeNull();
    });

    it("counts the pools online when all are", () => {
      const c = drawCard({ pools: [pool("rpool"), pool("tank"), pool("bulk")] });
      expect(c.getByText("3 online")).not.toHaveClass("text-warn");
    });

    it("names the state of a pool that is not online, in its health's colour", () => {
      const c = drawCard({ pools: [pool("rpool"), pool("tank", "DEGRADED"), pool("bulk")] });
      expect(c.getByText("1 of 3 degraded")).toHaveClass("text-warn");
    });

    it("counts the healthy disks", () => {
      expect(drawCard({ disks: [disk(1), disk(2)] }).getByText("2 healthy")).toBeInTheDocument();
    });

    it("counts a failing disk in crit", () => {
      const c = drawCard({ disks: [disk(1), disk(2, { healthy: false }), disk(3)] });
      expect(c.getByText("1 of 3 failing")).toHaveClass("text-crit");
    });

    it("leaves out pools and disks when the host has none", () => {
      const c = drawCard({ pools: [], disks: [] });
      expect(c.queryByText("Pools")).toBeNull();
      expect(c.queryByText("Disks")).toBeNull();
      expect(c.getByText("Power")).toBeInTheDocument();
    });
  });

  describe("the machines", () => {
    const card = (name: RegExp) => within(screen.getByRole("group", { name }));

    it("draw in the fleet grid after the host, which stays detailed below", () => {
      drawAt("/hardware", [host()], [machine("nas", true)]);
      const links = screen.getAllByRole("link", { name: /^Host / });
      expect(links).toHaveLength(1);
      const nas = screen.getByRole("group", { name: "Machine nas, up" });
      expect(links[0]?.compareDocumentPosition(nas)).toBe(Node.DOCUMENT_POSITION_FOLLOWING);
      expect(screen.getByRole("heading", { name: "Disk bays" })).toBeInTheDocument();
    });

    it("show a Linux machine's node-exporter stats, temperature included", () => {
      drawAt("/hardware", [host()], [machine("nas", true, "prometheus", linuxStats())]);
      const nas = card(/^Machine nas, up$/);
      expect(nas.getByText("Up")).toBeInTheDocument();
      expect(nas.getByText("Linux · x86_64")).toBeInTheDocument();
      expect(nas.getByRole("img", { name: "CPU 25%" })).toBeInTheDocument();
      expect(nas.getByText("8 cores")).toBeInTheDocument();
      expect(nas.getByText("Memory")).toBeInTheDocument();
      expect(nas.getByText("Root disk")).toBeInTheDocument();
      expect(nas.getByText("0.50 · 0.75 · 1.00")).toBeInTheDocument();
      expect(nas.getByText(formatDuration(90_000))).toBeInTheDocument();
      expect(nas.getByText("Temperature")).toBeInTheDocument();
      expect(nas.getByText("54°C")).toBeInTheDocument();
    });

    it("show a macOS machine's stats without a temperature it does not report", () => {
      drawAt("/hardware", [host()], [machine("studio", true, "prometheus", darwinStats())]);
      const studio = card(/^Machine studio, up$/);
      expect(studio.getByText("Darwin · arm64")).toBeInTheDocument();
      expect(studio.getByText("Root disk")).toBeInTheDocument();
      expect(studio.queryByText("Temperature")).toBeNull();
    });

    it("show a Gatus machine as up or down and its source only", () => {
      drawAt("/hardware", [host()], [machine("edge-gw", true, "gatus")]);
      const gw = card(/^Machine edge-gw, up$/);
      expect(gw.getByText("gatus")).toBeInTheDocument();
      expect(gw.getByText("Up")).toBeInTheDocument();
      expect(gw.queryByText("Memory")).toBeNull();
      expect(gw.queryByRole("img", { name: /^CPU/ })).toBeNull();
    });

    it("name their source when the stats carry no OS or arch", () => {
      const stats = linuxStats({ os: "", arch: "" });
      drawAt("/hardware", [host()], [machine("box", true, "prometheus", stats)]);
      const box = card(/^Machine box, up$/);
      expect(box.getByText("prometheus")).toBeInTheDocument();
      expect(box.queryByText("·")).toBeNull();
      expect(box.getByText("0.50 · 0.75 · 1.00")).toBeInTheDocument();
    });

    it("show a machine that is down", () => {
      drawAt("/hardware", [host()], [machine("nas", false)]);
      const nas = card(/^Machine nas, down$/);
      expect(nas.getByText("Down")).toBeInTheDocument();
      expect(nas.getByText("prometheus")).toBeInTheDocument();
    });

    it("leave out the figures a machine does not report", () => {
      const stats = linuxStats({
        cpus: 0,
        memUsedBytes: 0,
        memTotalBytes: 0,
        diskUsedBytes: 0,
        diskTotalBytes: 0,
        uptimeSeconds: 0,
        load1: 0.25,
      });
      drawAt("/hardware", [host()], [machine("box", true, "prometheus", stats)]);
      const box = card(/^Machine box, up$/);
      expect(box.queryByText("Memory")).toBeNull();
      expect(box.queryByText("Root disk")).toBeNull();
      expect(box.queryByRole("img", { name: /^CPU/ })).toBeNull();
      expect(box.queryByText(/cores?$/)).toBeNull();
      expect(box.queryByText("Uptime")).toBeNull();
      expect(box.getByText("0.25 · 0.75 · 1.00")).toBeInTheDocument();
      expect(box.getByText("54°C")).toBeInTheDocument();
    });

    it("wait for hardware.hosts before drawing, so configured hosts are never left out", () => {
      renderApp(<HardwarePage />);
      stream.plugins([hardware()]);
      stream.send("hardware.machines", { machines: [machine("nas", true)] });
      expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
      expect(screen.queryByRole("group", { name: "Machine nas, up" })).toBeNull();
    });

    it("are the whole page when the plugin publishes no hosts", () => {
      drawAt("/hardware", [], [machine("nas", true)]);
      expect(screen.getByRole("group", { name: "Machine nas, up" })).toBeInTheDocument();
      expect(screen.queryByRole("link", { name: /^Host / })).toBeNull();
      expect(screen.queryByRole("heading", { name: "Disk bays" })).toBeNull();
      expect(document.querySelector("[aria-busy=true]")).toBeNull();
    });

    it("keep the skeleton until something has arrived", () => {
      renderApp(<HardwarePage />);
      stream.plugins([hardware()]);
      expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
      stream.send("hardware.machines", { machines: [] });
      stream.send("hardware.hosts", { hosts: [] });
      // No machines and no host: still nothing to draw, so still waiting.
      expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    });

    it("show the plugin's source chips", async () => {
      renderApp(<HardwarePage />);
      stream.plugins([hardware()]);
      stream.send("hardware.hosts", { hosts: [] });
      stream.send("hardware.machines", { machines: [machine("pi", false, "gatus")] }, [
        { name: "gatus", ok: false, error: "not_configured", checkedAt: "2026-01-01T00:00:00Z" },
      ]);
      expect(await screen.findByText(/source not configured: gatus/)).toBeInTheDocument();
    });
  });

  it("shows the skeleton until hardware.hosts arrives", () => {
    renderApp(<HardwarePage />);
    stream.plugins([hardware()]);
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(screen.queryByRole("heading", { name: "Disk bays" })).toBeNull();
  });
});
