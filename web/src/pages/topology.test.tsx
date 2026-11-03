import { act, screen } from "@testing-library/react";
import type * as Motion from "motion/react";
import { afterEach, beforeEach, describe, expect, it, type MockInstance, vi } from "vitest";
import type { Group, Health, Topology, TopologyGroup, TopologyVM } from "@/api/types";
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

// jsdom has no layout: these tests give every card a fixed box and fire ResizeObserver
// themselves, the way the browser does when an observed element changes size.
interface Box {
  left: number;
  top: number;
  width: number;
  height: number;
}
const boxes = new Map<Element, Box>();
const observers = new Set<FakeResizeObserver>();

class FakeResizeObserver {
  readonly targets = new Set<Element>();
  readonly callback: ResizeObserverCallback;
  constructor(callback: ResizeObserverCallback) {
    this.callback = callback;
  }
  observe(target: Element) {
    observers.add(this);
    this.targets.add(target);
  }
  unobserve(target: Element) {
    this.targets.delete(target);
  }
  disconnect() {
    observers.delete(this);
    this.targets.clear();
  }
}

/** The browser reports that `el` changed size: every observer watching it runs. */
function fireResize(el: Element) {
  act(() => {
    for (const o of [...observers]) {
      if (o.targets.has(el)) o.callback([], o as unknown as ResizeObserver);
    }
  });
}

const k8sNode = (vmid: number, name: string): TopologyVM => ({
  vmid,
  name,
  health: "ok",
  cpuRatio: 0.2,
  memRatio: 0.5,
  isK8sNode: true,
  running: true,
});
const spreadGroup = (name: Group, namespace: string): TopologyGroup => ({
  name,
  health: "ok",
  namespaces: [{ name: namespace, health: "ok", pods: 3 }],
  podsByNode: { "node-1": 1, "node-2": 1, "node-3": 1 },
});
// Three Kubernetes nodes, each running pods of all three groups: 3 host beams and 9 more.
const wiredTopology: Topology = {
  ...topology("ok", 50),
  vms: [k8sNode(101, "node-1"), k8sNode(102, "node-2"), k8sNode(103, "node-3")],
  groups: [
    spreadGroup("Platform", "ns-a"),
    spreadGroup("Apps", "ns-b"),
    spreadGroup("Tenants", "ns-c"),
  ],
};

/** A beam's first and last point, from the path it draws. */
function ends(beam: Element) {
  const n = (
    beam
      .querySelector("path")
      ?.getAttribute("d")
      ?.match(/-?\d+(\.\d+)?/g) ?? []
  ).map(Number);
  return { from: [n[0], n[1]], to: [n[n.length - 2], n[n.length - 1]] };
}

function expectIncreasing(ys: number[]) {
  for (let i = 1; i < ys.length; i++) expect(ys[i]).toBeGreaterThan(ys[i - 1]);
}

describe("the topology beams between laid-out cards", () => {
  let measure: MockInstance<() => DOMRect>;
  beforeEach(() => {
    vi.stubGlobal("ResizeObserver", FakeResizeObserver);
    measure = vi.spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
      this: Element,
    ) {
      const { left, top, width, height } = boxes.get(this) ?? {
        left: 0,
        top: 0,
        width: 0,
        height: 0,
      };
      const rect = {
        left,
        top,
        width,
        height,
        x: left,
        y: top,
        right: left + width,
        bottom: top + height,
      };
      return { ...rect, toJSON: () => rect };
    });
  });
  afterEach(() => {
    measure.mockRestore();
    vi.unstubAllGlobals();
    boxes.clear();
    observers.clear();
  });

  // Three 200 px columns, 48 px gutters: host at x 0, VMs at x 248, groups at x 496.
  const inside = (y: number, card: Element) => {
    const box = boxes.get(card);
    return box !== undefined && y > box.top && y < box.top + box.height;
  };
  const layOut = () => {
    motionPrefs.reduced = true;
    window.matchMedia = (query: string) => ({
      ...realMatchMedia(query),
      matches: query.includes("min-width"),
    });
    renderApp(<TopologyPanel topology={wiredTopology} />);
    const card = (name: string) => {
      const wrapper = screen.getByText(name).closest('[data-slot="panel"]')?.parentElement;
      if (!wrapper) throw new Error(`no card for ${name}`);
      return wrapper;
    };
    const cards = {
      host: card("pve-1"),
      vms: ["node-1", "node-2", "node-3"].map(card),
      groups: ["Platform", "Apps", "Tenants"].map(card),
    };
    const beams = [...document.querySelectorAll('svg[data-slot="beam"]')];
    const layer = beams[0].parentElement as Element;
    const container = layer.parentElement as Element;
    boxes.set(container, { left: 0, top: 0, width: 696, height: 400 });
    boxes.set(cards.host, { left: 0, top: 140, width: 200, height: 120 });
    for (const [i, c] of cards.vms.entries()) {
      boxes.set(c, { left: 248, top: 10 + i * 130, width: 200, height: 120 });
    }
    for (const [i, c] of cards.groups.entries()) {
      boxes.set(c, { left: 496, top: 40 + i * 110, width: 200, height: 100 });
    }
    fireResize(container);
    return { cards, beams, layer };
  };

  it("fans the host's beams out in the VMs' top-to-bottom order", () => {
    const { beams } = layOut();
    const host = beams.map(ends).filter((b) => b.from[0] === 200);
    expect(host).toHaveLength(3);
    expectIncreasing(host.sort((a, b) => a.to[1] - b.to[1]).map((b) => b.from[1]));
  });

  it("fans each VM's beams out and each group's beams in, in card order", () => {
    const { cards, beams } = layOut();
    const k8s = beams.map(ends).filter((b) => b.from[0] === 448);
    expect(k8s).toHaveLength(9);
    for (const vm of cards.vms) {
      const out = k8s.filter((b) => inside(b.from[1], vm)).sort((a, b) => a.to[1] - b.to[1]);
      expect(out).toHaveLength(3);
      expectIncreasing(out.map((b) => b.from[1]));
    }
    for (const group of cards.groups) {
      const into = k8s.filter((b) => inside(b.to[1], group)).sort((a, b) => a.from[1] - b.from[1]);
      expect(into).toHaveLength(3);
      expectIncreasing(into.map((b) => b.to[1]));
    }
  });

  it("draws the beams on a layer below the cards, and each card on an opaque base", () => {
    const { cards, layer } = layOut();
    const grid = cards.host.parentElement?.parentElement as Element;
    expect(grid.parentElement).toBe(layer.parentElement);
    // Painted first, and with the lower z-index of the two positioned siblings.
    expect(layer.compareDocumentPosition(grid) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    const z = (el: Element) =>
      Number(/(?:^|\s)z-(\d+)(?:\s|$)/.exec(el.getAttribute("class") ?? "")?.[1]);
    expect(z(layer)).toBeLessThan(z(grid));
    for (const card of [cards.host, ...cards.vms, ...cards.groups]) {
      expect(card).toHaveClass("bg-panel-solid");
    }
  });
});
