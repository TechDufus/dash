import { screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { KPI, OverviewData } from "@/api/types";
import { contributesPanel, Slot } from "@/plugins/slots";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";

// Motion reads prefers-reduced-motion once; with it on a KPI tile prints its value at once.
const matchMedia = window.matchMedia;
window.matchMedia = (query: string) => ({
  ...matchMedia(query),
  matches: query.includes("prefers-reduced-motion"),
});

const row = (state = "healthy") =>
  pluginInfo({
    id: "flux",
    state,
    topics: ["flux.objects"],
    nav: [{ path: "/gitops", label: "GitOps", title: "GitOps (Flux)", icon: "GitBranch" }],
  });

const kpi = (key: string, value: number, health: KPI["health"] = "ok"): KPI => ({
  key,
  plugin: "flux",
  label: key,
  unit: "",
  value,
  health,
});

const overview = (kpiList: KPI[]): OverviewData =>
  ({
    status: "ok",
    attention: [],
    kpiList,
    topology: { host: {}, vms: [], groups: [] },
  }) as unknown as OverviewData;

const LINK = { name: "GitOps 8 of 9 ready. Open GitOps" };

/** The tile's health dot (StatusDot labels itself with the health). */
const dot = (tile: HTMLElement, label: "OK" | "Warning") =>
  within(tile).queryByRole("img", { name: label });

describe("the GitOps overview tile", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch({ "GET /api/v1/me": { user: "u", role: "admin", scopes: ["read"] } });
    stream = connectStream();
  });
  afterEach(() => stream.close());

  function show(kpiList: KPI[], state = "healthy") {
    renderApp(<Slot id="overview.kpis" data={overview(kpiList)} />);
    stream.plugins([row(state)]);
  }

  it("claims the plugin's overview tile", () => {
    expect(contributesPanel("flux", "overview.kpis")).toBe(true);
  });

  it("reads its numbers from kpiList, and is ok when everything is ready and verified", async () => {
    show([kpi("ready", 9), kpi("total", 9), kpi("suspended", 0), kpi("verified", 1)]);
    const tile = await screen.findByRole("link", { name: "GitOps 9 of 9 ready. Open GitOps" });
    expect(tile.getAttribute("href")).toBe("/gitops");
    expect(tile.textContent).toContain("/ 9");
    expect(tile.textContent).toContain("Source verified");
    expect(screen.getByRole("img", { name: "Source verified" })).toBeTruthy();
  });

  it("ignores the KPIs of another plugin", () => {
    show([{ ...kpi("ready", 1), plugin: "other" }, kpi("total", 1)]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("warns when fewer objects are ready than there are, and says what is suspended", async () => {
    show([kpi("ready", 8), kpi("total", 9), kpi("suspended", 1), kpi("verified", 1)]);
    const tile = await screen.findByRole("link", LINK);
    expect(tile.textContent).toContain("1 suspended");
    expect(tile.textContent).toContain("Source verified");
    expect(dot(tile, "Warning")).not.toBeNull();
  });

  it("warns, and says so, when a verified source is not verified", async () => {
    show([kpi("ready", 9), kpi("total", 9), kpi("suspended", 0), kpi("verified", 0, "crit")]);
    const tile = await screen.findByRole("link", { name: "GitOps 9 of 9 ready. Open GitOps" });
    expect(tile.textContent).toContain("Source not verified");
    expect(screen.getByRole("img", { name: "Source not verified" })).toBeTruthy();
    expect(dot(tile, "Warning")).not.toBeNull();
  });

  it("claims nothing about verification while the KPI is not published", async () => {
    show([kpi("ready", 9), kpi("total", 9), kpi("suspended", 0)]);
    const tile = await screen.findByRole("link", { name: "GitOps 9 of 9 ready. Open GitOps" });
    expect(tile.textContent).not.toMatch(/verified/i);
    expect(within(tile).queryByRole("img", { name: /verified/i })).toBeNull();
    expect(dot(tile, "Warning")).toBeNull();
    expect(dot(tile, "OK")).not.toBeNull();
  });

  it("draws nothing while the counts are not published, or with its plugin hidden", () => {
    show([kpi("ready", 9)]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
    stream.plugins([row("unavailable")]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });
});
