import { screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Disk, Fan, HostData, PluginInfo } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { fanScale, HardwarePage, MAX_BAYS } from "./hardware-page";
import type { Machine } from "./model.gen";

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

const machine = (name: string, up: boolean, source = "prometheus"): Machine => ({
  ref: `hardware:Machine/${name}`,
  name,
  up,
  source,
});

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
    stream.send("hardware.hosts", data);
  };

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

  describe("the machines", () => {
    it("draw below the host panels, with name, source and Up or Down", async () => {
      renderApp(<HardwarePage />);
      stream.plugins([hardware()]);
      stream.send("hardware.hosts", host());
      stream.send("hardware.machines", {
        machines: [machine("nas", true), machine("pi", false, "gatus")],
      });
      expect(await screen.findByRole("heading", { name: "Other machines" })).toBeInTheDocument();
      const nas = screen.getByText("nas").closest("li") as HTMLElement;
      expect(within(nas).getByText("prometheus")).toBeInTheDocument();
      expect(within(nas).getByText("Up")).toBeInTheDocument();
      const pi = screen.getByText("pi").closest("li") as HTMLElement;
      expect(within(pi).getByText("gatus")).toBeInTheDocument();
      expect(within(pi).getByText("Down")).toBeInTheDocument();
      expect(screen.getByRole("heading", { name: "Disk bays" })).toBeInTheDocument();
    });

    it("are hidden when none is configured", () => {
      renderApp(<HardwarePage />);
      stream.plugins([hardware()]);
      stream.send("hardware.hosts", host());
      stream.send("hardware.machines", { machines: [] });
      expect(screen.getByRole("heading", { name: "Disk bays" })).toBeInTheDocument();
      expect(screen.queryByRole("heading", { name: "Other machines" })).toBeNull();
      expect(screen.queryByText(/No other machines/)).toBeNull();
    });

    it("are the whole page when no host is configured and hardware.hosts never arrives", async () => {
      renderApp(<HardwarePage />);
      stream.plugins([hardware()]);
      stream.send("hardware.machines", { machines: [machine("nas", true)] });
      expect(await screen.findByRole("heading", { name: "Machines" })).toBeInTheDocument();
      expect(screen.getByText("nas")).toBeInTheDocument();
      // No skeleton waiting for a host that is never coming, and no host panels.
      expect(document.querySelector("[aria-busy=true]")).toBeNull();
      expect(screen.queryByRole("heading", { name: "Disk bays" })).toBeNull();
    });

    it("keep the skeleton until something has arrived", () => {
      renderApp(<HardwarePage />);
      stream.plugins([hardware()]);
      expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
      stream.send("hardware.machines", { machines: [] });
      // Empty machines and no host: still nothing to draw, so still waiting.
      expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    });

    it("show the plugin's source chips", async () => {
      renderApp(<HardwarePage />);
      stream.plugins([hardware()]);
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
