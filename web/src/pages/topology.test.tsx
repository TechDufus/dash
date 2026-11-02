import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { Health, Topology } from "@/api/types";
import { formatCelsius } from "@/lib/format";
import { renderApp } from "@/test/harness";
import { TopologyPanel } from "./topology";

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
