import { screen } from "@testing-library/react";
import type * as Motion from "motion/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Health, Topology } from "@/api/types";
import { formatCelsius } from "@/lib/format";
import { renderApp } from "@/test/harness";
import { TopologyPanel } from "./topology";

// Motion reads prefers-reduced-motion once per module, so the beam tests set the user's
// preference here instead of through matchMedia.
const motionPrefs = vi.hoisted(() => ({ reduced: false }));
vi.mock("motion/react", async (importOriginal) => ({
  ...(await importOriginal<typeof Motion>()),
  useReducedMotion: () => motionPrefs.reduced,
}));

const topology = (cpuTempHealth: Health, cpuTempC: number): Topology => ({
  host: {
    name: "pve-1",
    health: "ok",
    powerWatts: 200,
    cpuTempC,
    cpuTempHealth,
    memUsedRatio: 0.4,
  },
  vms: [],
  groups: [],
});

const realMatchMedia = window.matchMedia;

afterEach(() => {
  window.matchMedia = realMatchMedia;
});

// The server judges the CPU temperature against the host's own thresholds; the page only colours
// it by that verdict, so a hot number can be fine and a cool one can be critical. The cards draw
// from 768 px up (HostCard) and a compact list below it (HostRow).
describe.each([
  ["the host card", true],
  ["the compact host row", false],
])("%s", (_name, isDesktop) => {
  const draw = (health: Health, cpuTempC: number) => {
    window.matchMedia = (query: string) => ({ ...realMatchMedia(query), matches: isDesktop });
    renderApp(<TopologyPanel topology={topology(health, cpuTempC)} />);
    expect(screen.queryByText("CPU temp") !== null).toBe(isDesktop); // the card has the label, the row does not
    return screen.getByText(formatCelsius(cpuTempC));
  };

  it("shows a critical CPU temperature as critical, whatever the number", () => {
    const temp = draw("crit", 41);
    expect(temp).toHaveClass("text-crit");
    expect(temp).not.toHaveClass("text-ok");
  });

  it("shows an ok CPU temperature as ok, whatever the number", () => {
    const temp = draw("ok", 97);
    expect(temp).toHaveClass("text-ok");
    expect(temp).not.toHaveClass("text-crit");
  });

  it("shows a warning CPU temperature as a warning", () => {
    expect(draw("warn", 70)).toHaveClass("text-warn");
  });
});

// One running Kubernetes node feeding one group, and one stopped VM: three beams.
const beamTopology: Topology = {
  ...topology("ok", 50),
  vms: [
    {
      vmid: 101,
      name: "node-1",
      health: "ok",
      cpuRatio: 0.2,
      memRatio: 0.5,
      isK8sNode: true,
      running: true,
    },
    {
      vmid: 120,
      name: "spare",
      health: "unknown",
      cpuRatio: 0,
      memRatio: 0,
      isK8sNode: false,
      running: false,
    },
  ],
  groups: [
    {
      name: "Apps",
      health: "ok",
      namespaces: [{ name: "web", health: "ok", pods: 3 }],
      podsByNode: { "node-1": 3 },
    },
  ],
};

describe("the topology beams", () => {
  const draw = (reduced: boolean) => {
    motionPrefs.reduced = reduced;
    window.matchMedia = (query: string) => ({
      ...realMatchMedia(query),
      matches: query.includes("min-width"),
    });
    const { container } = renderApp(<TopologyPanel topology={beamTopology} />);
    const beams = [...container.querySelectorAll('svg[data-slot="beam"]')];
    return {
      beams,
      pulsed: beams.filter((b) => b.querySelector("linearGradient")),
      dashed: beams.filter((b) => b.querySelector("path[stroke-dasharray]")),
    };
  };

  it("pulses along the running VMs' beams and draws the stopped VM's beam dashed and still", () => {
    const { beams, pulsed, dashed } = draw(false);
    expect(beams).toHaveLength(3); // host -> node-1, host -> spare, node-1 -> Apps
    expect(pulsed).toHaveLength(2);
    expect(dashed).toHaveLength(1);
    expect(dashed[0].querySelector("linearGradient")).toBeNull();
  });

  it("keeps every beam but drops the pulses when the user prefers reduced motion", () => {
    const { beams, pulsed, dashed } = draw(true);
    expect(beams).toHaveLength(3);
    expect(pulsed).toHaveLength(0);
    expect(dashed).toHaveLength(1);
  });
});
