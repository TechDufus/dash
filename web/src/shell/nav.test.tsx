import { act, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import {
  envelope,
  type FakeEventSource,
  installEventSource,
  pluginInfo,
  pluginsEnvelope,
  renderApp,
  stubFetch,
} from "@/test/harness";
import { RAIL_LABELS, shimRows, withState } from "@/test/shims";
import { Rail } from "./rail";
import { TopBar } from "./topbar";

let ES: typeof FakeEventSource;

beforeEach(() => {
  ES = installEventSource();
  stubFetch({
    "GET /api/v1/me": { user: "dev", role: "admin", version: "test", ui: { iconBaseUrl: "" } },
    "GET /api/v1/plugins": () => new Promise(() => {}), // the REST copy never answers
  });
});

afterEach(() => {
  stopStream();
});

function connect(): FakeEventSource {
  startStream();
  const es = ES.instances.at(-1);
  if (!es) throw new Error("no EventSource");
  es.open();
  return es;
}

function railLabels(): string[] {
  const rail = screen.getByRole("navigation", { name: "Primary" });
  return within(rail)
    .getAllByRole("link")
    .map((a) => a.getAttribute("aria-label") ?? "")
    .filter((l) => l !== "dash home");
}

describe("rail", () => {
  it("shows today's nine entries, in today's order, for the nine shim rows", () => {
    const es = connect();
    renderApp(<Rail />);
    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));
    expect(railLabels()).toEqual([...RAIL_LABELS]);
    const hrefs = within(screen.getByRole("navigation", { name: "Primary" }))
      .getAllByRole("link")
      .map((a) => a.getAttribute("href"));
    expect(hrefs).toEqual([
      "/",
      "/",
      "/vms",
      "/cluster",
      "/gitops",
      "/hardware",
      "/storage",
      "/alerts",
      "/status",
      "/apps",
    ]);
  });

  it("drops a plugin's entry when its row goes disabled, without a reload", () => {
    const es = connect();
    renderApp(<Rail />);
    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));
    expect(railLabels()).toContain("VMs");

    act(() =>
      es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "proxmox", "disabled"))),
    );
    expect(railLabels()).toEqual(RAIL_LABELS.filter((l) => l !== "VMs"));
    expect(ES.instances).toHaveLength(1);

    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));
    expect(railLabels()).toEqual([...RAIL_LABELS]);
  });

  it.each(["unavailable", "unconfigured", "blocked"])(
    "hides an entry whose plugin is %s",
    (state) => {
      const es = connect();
      renderApp(<Rail />);
      act(() => es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "gatus", state))));
      expect(railLabels()).not.toContain("Status");
    },
  );

  it.each(["starting", "degraded", "error"])("keeps an entry whose plugin is %s", (state) => {
    const es = connect();
    renderApp(<Rail />);
    act(() => es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "gatus", state))));
    expect(railLabels()).toContain("Status");
  });

  it("shows the core entries and three placeholders until the plugin list arrives", () => {
    renderApp(<Rail />);
    expect(railLabels()).toEqual(["Overview", "Storage", "Apps"]);
    const rail = screen.getByRole("navigation", { name: "Primary" });
    expect(rail.querySelectorAll("[data-placeholder]")).toHaveLength(3);
    // No plugin entry, and no layout shift: placeholders sit where the plugin entries start.
    const items = [...rail.querySelectorAll("li")];
    expect(items.slice(1, 4).every((li) => li.hasAttribute("data-placeholder"))).toBe(true);
  });

  it("removes the placeholders once the list is known", () => {
    const es = connect();
    renderApp(<Rail />);
    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));
    expect(
      screen.getByRole("navigation", { name: "Primary" }).querySelectorAll("[data-placeholder]"),
    ).toHaveLength(0);
  });

  it("drops an entry no module can render, with one console warning", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const es = connect();
    renderApp(<Rail />);
    const rows = [
      ...shimRows(),
      pluginInfo({
        id: "mystery",
        nav: [{ path: "/mystery-page", label: "Mystery", title: "Mystery", icon: "Puzzle" }],
      }),
    ];
    act(() => es.emit("core.plugins", pluginsEnvelope(rows)));
    act(() => es.emit("core.plugins", pluginsEnvelope(withState(rows, "gatus", "degraded"))));
    expect(railLabels()).toEqual([...RAIL_LABELS]);
    const mine = warn.mock.calls.filter((c) => String(c[0]).includes("/mystery-page"));
    expect(mine).toHaveLength(1);
    warn.mockRestore();
  });

  it("marks the entry of the current page", () => {
    const es = connect();
    renderApp(<Rail />, { path: "/cluster/pods/media/web" });
    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));
    const rail = screen.getByRole("navigation", { name: "Primary" });
    expect(within(rail).getByRole("link", { name: "Cluster" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(within(rail).getByRole("link", { name: "Overview" })).not.toHaveAttribute(
      "aria-current",
    );
  });

  it("draws the firing count from core.overview on the Alerts entry only", () => {
    const es = connect();
    renderApp(<Rail />);
    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));
    const rail = screen.getByRole("navigation", { name: "Primary" });
    expect(within(within(rail).getByRole("link", { name: "Alerts" })).queryByText("3")).toBeNull();

    act(() =>
      es.emit(
        "core.overview",
        envelope("core.overview", { status: "warn", attention: [], kpis: { alertsFiring: 3 } }),
      ),
    );
    expect(
      within(within(rail).getByRole("link", { name: "Alerts" })).getByText("3"),
    ).toBeInTheDocument();
    expect(within(within(rail).getByRole("link", { name: "Status" })).queryByText("3")).toBeNull();

    act(() =>
      es.emit(
        "core.overview",
        envelope("core.overview", { status: "ok", attention: [], kpis: { alertsFiring: 0 } }),
      ),
    );
    expect(within(within(rail).getByRole("link", { name: "Alerts" })).queryByText("3")).toBeNull();
  });

  it("takes the badge away with the entry when alertmanager is switched off", () => {
    const es = connect();
    renderApp(<Rail />);
    act(() => {
      es.emit("core.plugins", pluginsEnvelope(shimRows()));
      es.emit(
        "core.overview",
        envelope("core.overview", { status: "warn", attention: [], kpis: { alertsFiring: 2 } }),
      );
    });
    expect(screen.getByText("2")).toBeInTheDocument();
    act(() =>
      es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "alertmanager", "disabled"))),
    );
    expect(screen.queryByText("2")).toBeNull();
  });
});

describe("mobile navigation sheet", () => {
  it("lists the same entries as the rail and follows plugin state", async () => {
    const es = connect();
    renderApp(<TopBar />);
    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));

    await userEvent.click(screen.getByRole("button", { name: "Open navigation" }));
    const mobile = await screen.findByRole("navigation", { name: "Mobile" });
    expect(
      within(mobile)
        .getAllByRole("link")
        .map((a) => a.textContent),
    ).toEqual([...RAIL_LABELS]);

    act(() =>
      es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "hardware", "disabled"))),
    );
    expect(within(mobile).queryByRole("link", { name: "Hardware" })).toBeNull();
  });

  it("titles the page from the visible entries", () => {
    const es = connect();
    renderApp(<TopBar />, { path: "/gitops" });
    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("GitOps (Flux)");

    act(() => es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "flux", "disabled"))));
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("dash");
  });
});
