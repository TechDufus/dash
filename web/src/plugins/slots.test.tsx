import { act, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { HostData, OverviewData } from "@/api/types";
import { renderApp } from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
import { Slot } from "./slots";
import { connectStream, type TestStream } from "./test-util";
import type { ModulePanels, SlotProps } from "./types";

// The slot reads its contributions from PANELS; these tests fill it with panels that only print
// their name, so they test the slot (order, visibility, isolation), not the real panels.
const mocked = vi.hoisted(() => ({ PANELS: {} as Record<string, ModulePanels> }));
vi.mock("./panels", () => ({ PANELS: mocked.PANELS }));

const data = { kpis: {}, attention: [], kpiList: [] } as unknown as OverviewData;

function label(text: string) {
  // biome-ignore lint/correctness/noComponentHookFactories: each contribution needs its own component; the factory runs per test, never during a render
  return function Label(_: SlotProps["overview.kpis"]) {
    return <p>{text}</p>;
  };
}

function Kpis() {
  return <Slot id="overview.kpis" data={data} />;
}

/** Text of every `<p>` in document order. */
function rendered(): string[] {
  return [...document.querySelectorAll("p")].map((p) => p.textContent ?? "");
}

describe("Slot", () => {
  let stream: TestStream;

  beforeEach(() => {
    for (const k of Object.keys(mocked.PANELS)) delete mocked.PANELS[k];
    stream = connectStream();
  });
  afterEach(() => stream.close());

  it("orders panels by their order, then the module's order, then registry and declaration position", () => {
    mocked.PANELS.alertmanager = {
      "overview.kpis": [{ id: "a", order: 50, component: label("alertmanager") }],
    };
    mocked.PANELS.hardware = {
      // No order of its own: the module's order (40) stands in.
      "overview.kpis": [{ id: "h", component: label("hardware") }],
    };
    mocked.PANELS.kubernetes = {
      "overview.kpis": [{ id: "k", order: 10, component: label("kubernetes") }],
    };
    mocked.PANELS.flux = {
      // Ties with kubernetes at 10; kubernetes comes first in the registry.
      "overview.kpis": [{ id: "f", order: 10, component: label("flux") }],
    };
    mocked.PANELS.proxmox = {
      // Module order 10 for both; declaration order decides, and proxmox precedes kubernetes.
      "overview.kpis": [
        { id: "p1", component: label("proxmox 1") },
        { id: "p2", component: label("proxmox 2") },
      ],
    };
    renderApp(<Kpis />);
    stream.plugins(shimRows());
    expect(rendered()).toEqual([
      "proxmox 1",
      "proxmox 2",
      "kubernetes",
      "flux",
      "hardware",
      "alertmanager",
    ]);
  });

  it("renders nothing until the plugin list has arrived", () => {
    mocked.PANELS.flux = { "overview.kpis": [{ id: "f", order: 1, component: label("flux") }] };
    renderApp(<Kpis />);
    expect(rendered()).toEqual([]);
    stream.plugins(shimRows());
    expect(rendered()).toEqual(["flux"]);
  });

  it("leaves out the panels of a module whose plugin is hidden, and brings them back", () => {
    mocked.PANELS.flux = { "overview.kpis": [{ id: "f", order: 1, component: label("flux") }] };
    mocked.PANELS.hardware = {
      "overview.kpis": [{ id: "h", order: 2, component: label("hardware") }],
    };
    renderApp(<Kpis />);
    const rows = shimRows();
    stream.plugins(rows);
    expect(rendered()).toEqual(["flux", "hardware"]);

    for (const state of ["disabled", "unavailable", "unconfigured", "blocked"]) {
      stream.plugins(withState(rows, "hardware", state));
      expect(rendered(), state).toEqual(["flux"]);
    }
    stream.plugins(withState(rows, "hardware", "degraded"));
    expect(rendered()).toEqual(["flux", "hardware"]);

    // A module the server does not list has no plugin behind it.
    stream.plugins(shimRows().filter((r) => r.id === "hardware"));
    expect(rendered()).toEqual(["hardware"]);
  });

  it("hides a panel whose topic no visible plugin publishes, but never one that reads core.*", () => {
    mocked.PANELS.proxmox = {
      "overview.kpis": [
        { id: "needs-storage", order: 1, reads: ["storage"], component: label("needs storage") },
        { id: "core", order: 2, reads: ["core.overview"], component: label("core") },
      ],
    };
    renderApp(<Kpis />);
    const rows = shimRows();
    stream.plugins(rows);
    expect(rendered()).toEqual(["needs storage", "core"]);

    // kubernetes publishes storage; hidden, the topic is gone and so is the panel.
    stream.plugins(withState(rows, "kubernetes", "disabled"));
    expect(rendered()).toEqual(["core"]);
  });

  it("isolates a panel that throws: a muted row replaces it and its siblings stay", () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    mocked.PANELS.flux = {
      "overview.kpis": [
        { id: "before", order: 1, component: label("before") },
        {
          id: "broken",
          order: 2,
          component: () => {
            throw new Error("bad payload");
          },
        },
        { id: "after", order: 3, component: label("after") },
      ],
    };
    renderApp(<Kpis />);
    stream.plugins(shimRows());

    expect(rendered()).toEqual(["before", "after"]);
    expect(screen.getAllByTestId("panel-failed")).toHaveLength(1);
    expect(screen.getByText("This panel failed to render.")).toBeInTheDocument();
    expect(error.mock.calls.some((c) => String(c[0]).includes("flux/broken"))).toBe(true);
  });

  it("tries a failed panel again when its props change", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    mocked.PANELS.flux = {
      "overview.kpis": [
        {
          id: "picky",
          order: 1,
          component: ({ data: d }) => {
            if (d.status === "crit") throw new Error("cannot draw crit");
            return <p>fine</p>;
          },
        },
      ],
    };
    const view = renderApp(<Slot id="overview.kpis" data={{ ...data, status: "crit" }} />);
    stream.plugins(shimRows());
    expect(screen.getByTestId("panel-failed")).toBeInTheDocument();

    act(() => view.rerender(<Slot id="overview.kpis" data={{ ...data, status: "ok" }} />));
    expect(screen.queryByTestId("panel-failed")).toBeNull();
    expect(rendered()).toEqual(["fine"]);
  });

  it("serves hardware.facts by the same rules: order, visibility, reads, props", () => {
    const fact = (text: string) =>
      // biome-ignore lint/correctness/noComponentHookFactories: each contribution needs its own component; the factory runs per test, never during a render
      function Fact({ host }: SlotProps["hardware.facts"]) {
        return (
          <p>
            {text}:{host?.name ?? "no host"}
          </p>
        );
      };
    const host = { name: "box" } as HostData;

    // Nothing contributes yet: the slot is empty, as the Hardware page will be until P3.7.
    renderApp(<Slot id="hardware.facts" host={host} />);
    const rows = shimRows();
    stream.plugins(rows);
    expect(rendered()).toEqual([]);

    mocked.PANELS.proxmox = {
      "hardware.facts": [
        { id: "late", order: 2, component: fact("late") },
        { id: "early", order: 1, component: fact("early") },
      ],
    };
    mocked.PANELS.kubernetes = {
      // Reads a topic proxmox publishes: hidden with proxmox, never waiting for data.
      "hardware.facts": [{ id: "k", order: 3, reads: ["vms"], component: fact("needs vms") }],
    };
    stream.plugins(shimRows()); // a new list makes the slot read the contributions again
    expect(rendered()).toEqual(["early:box", "late:box", "needs vms:box"]);

    stream.plugins(withState(rows, "proxmox", "disabled"));
    expect(rendered()).toEqual([]);
    stream.plugins(rows);
    expect(rendered()).toEqual(["early:box", "late:box", "needs vms:box"]);

    // The host prop is optional: before the topic arrives a panel gets none.
    renderApp(<Slot id="hardware.facts" />);
    stream.plugins(shimRows());
    expect(rendered().at(-1)).toBe("needs vms:no host");
  });

  it("gives storage panels a cell: full spans the row, half takes one column", () => {
    mocked.PANELS.kubernetes = {
      storage: [
        { id: "wide", order: 1, span: "full", component: () => <p>wide</p> },
        { id: "narrow", order: 2, component: () => <p>narrow</p> },
      ],
    };
    renderApp(<Slot id="storage" env={undefined} />);
    stream.plugins(shimRows());
    expect(screen.getByText("wide").closest("div.lg\\:col-span-2")).not.toBeNull();
    expect(screen.getByText("narrow").closest("div.lg\\:col-span-2")).toBeNull();
  });
});
