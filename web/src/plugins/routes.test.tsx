import { act, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import {
  envelope,
  type FakeEventSource,
  type FetchStub,
  installEventSource,
  pluginInfo,
  pluginsEnvelope,
  renderApp,
  stubFetch,
} from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
import { PluginDisabledPage, PluginRoutes } from "./routes";

let ES: typeof FakeEventSource;
let fetchStub: FetchStub;

beforeEach(() => {
  ES = installEventSource();
  fetchStub = stubFetch({ "GET /api/v1/plugins": () => new Promise(() => {}) });
});

afterEach(() => {
  stopStream();
});

function connect(rows = shimRows()): FakeEventSource {
  startStream();
  const es = ES.instances.at(-1);
  if (!es) throw new Error("no EventSource");
  es.open();
  es.emit("core.plugins", pluginsEnvelope(rows));
  return es;
}

const STATUS_PAGE = "No endpoints reported.";

describe("PluginRoutes", () => {
  it("renders a visible plugin's lazy page at its route", async () => {
    const es = connect();
    es.emit("gatus.status", envelope("gatus.status", { endpoints: [] }));
    renderApp(<PluginRoutes />, { path: "/status" });
    expect(await screen.findByText(STATUS_PAGE)).toBeInTheDocument();
  });

  it("passes route params to the page and matches the optional segment", async () => {
    const es = connect();
    es.emit("proxmox.vms", envelope("proxmox.vms", { vms: [], tasks: [] }));
    renderApp(<PluginRoutes />, { path: "/vms/101" });
    // The VM page asks for the detail of the VM named in the URL.
    await waitFor(() =>
      expect(fetchStub.calls.some((c) => c.path === "/api/v1/plugins/proxmox/vms/101")).toBe(true),
    );
  });

  it("shows a loading page, and mounts nothing, until the plugin list is known", () => {
    startStream();
    ES.instances[0]?.open();
    renderApp(<PluginRoutes />, { path: "/status" });
    expect(document.querySelector('[aria-busy="true"]')).not.toBeNull();
    expect(screen.queryByText(STATUS_PAGE)).toBeNull();
    expect(screen.queryByText(/switched off/)).toBeNull();
  });

  it("renders the disabled page at a deep link, not the 404 page", () => {
    connect(withState(shimRows(), "proxmox", "disabled"));
    renderApp(<PluginRoutes />, { path: "/vms/101" });
    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Proxmox VE is switched off.");
    expect(status).toHaveTextContent("plugins.proxmox.enabled");
    expect(screen.getByRole("link", { name: "Back to Overview" })).toHaveAttribute("href", "/");
    expect(screen.queryByText(/Nothing here/)).toBeNull();
  });

  it.each([
    ["unavailable", "Gatus is unavailable. no source reachable"],
    ["unconfigured", "Gatus is not configured. invalid configuration"],
    ["blocked", "Gatus is blocked. needs kubernetes"],
  ])("says what a %s plugin is", (state, text) => {
    const message = text.split(". ")[1] ?? "";
    connect(withState(shimRows(), "gatus", state, message));
    renderApp(<PluginRoutes />, { path: "/status" });
    expect(screen.getByRole("status")).toHaveTextContent(text);
  });

  // The row already says what to set; the page shows it once, not "is not configured. not
  // configured: set ...", for a plugin enabled without its settings and one off for lack of them.
  it.each(["unconfigured", "disabled"])("shows the hint of a %s row once", (state) => {
    connect(withState(shimRows(), "gatus", state, "not configured: set plugins.gatus.url"));
    renderApp(<PluginRoutes />, { path: "/status" });
    const text = screen.getByRole("status").textContent ?? "";
    expect(text).toContain("Gatus is not configured: set plugins.gatus.url.");
    expect(text.match(/not configured/g)).toHaveLength(1);
    expect(text).not.toContain("switched off");
  });

  it("still says a plugin that was switched off by its setting is switched off", () => {
    connect(withState(shimRows(), "gatus", "disabled", "disabled"));
    renderApp(<PluginRoutes />, { path: "/status" });
    const status = screen.getByRole("status");
    expect(status).toHaveTextContent("Gatus is switched off.");
    expect(status).toHaveTextContent("plugins.gatus.enabled");
  });

  it("does not print an empty message as a dangling space", () => {
    connect(withState(shimRows(), "gatus", "unavailable", ""));
    renderApp(<PluginRoutes />, { path: "/status" });
    expect(screen.getByRole("status").textContent).toContain(
      "Gatus is unavailable.Back to Overview",
    );
  });

  it("says so when the build has a module the server does not know", () => {
    connect(shimRows().filter((p) => p.id !== "flux"));
    renderApp(<PluginRoutes />, { path: "/gitops" });
    expect(screen.getByRole("status")).toHaveTextContent("This dash has no flux plugin.");
  });

  it("never mounts or fetches a hidden plugin's page", async () => {
    const es = connect(withState(shimRows(), "proxmox", "disabled"));
    es.emit("proxmox.vms", envelope("proxmox.vms", { vms: [], tasks: [] })); // data a hidden plugin should not show
    renderApp(<PluginRoutes />, { path: "/vms/101" });
    expect(screen.getByRole("status")).toHaveTextContent("Proxmox VE is switched off.");
    // The VM sheet would ask for /api/v1/plugins/proxmox/vms/101 the moment the page mounted.
    await new Promise((r) => setTimeout(r, 20));
    expect(fetchStub.calls).toEqual([]);
  });

  it("answers an unknown path with the 404 page", () => {
    connect();
    renderApp(<PluginRoutes />, { path: "/nope" });
    expect(screen.getByText(/Nothing here/)).toBeInTheDocument();
  });

  it("swaps a page for the disabled page when its plugin turns off, without a reload", async () => {
    const es = connect();
    es.emit("gatus.status", envelope("gatus.status", { endpoints: [] }));
    renderApp(<PluginRoutes />, { path: "/status" });
    await screen.findByText(STATUS_PAGE);

    act(() => es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "gatus", "disabled"))));
    expect(screen.queryByText(STATUS_PAGE)).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent("Gatus is switched off.");
    expect(ES.instances).toHaveLength(1);
  });

  it("gives each generic page its own error boundary", () => {
    const view = { id: "v", kind: "stat", title: "Depth", topic: "queue", path: "/n", columns: [] };
    const rows = [
      // A name that is not text makes this plugin's page throw while it renders.
      pluginInfo({ id: "broken", name: {} as unknown as string, generic: [view as never] }),
      pluginInfo({ id: "fine", name: "Fine plugin", generic: [view as never] }),
    ];
    const quiet = vi.spyOn(console, "error").mockImplementation(() => {});
    try {
      connect(rows);
      const { navigate } = renderApp(<PluginRoutes />, { path: "/x/broken" });
      expect(screen.getByRole("alert")).toHaveTextContent("This page failed to load.");

      act(() => navigate("/x/fine"));
      expect(screen.queryByRole("alert")).toBeNull();
      expect(screen.getByRole("heading", { name: "Fine plugin" })).toBeInTheDocument();
    } finally {
      quiet.mockRestore();
    }
  });

  it("renders the core pages whatever the plugins are", () => {
    connect([]);
    renderApp(<PluginRoutes />, { path: "/storage" });
    expect(screen.queryByText(/Nothing here/)).toBeNull();
  });
});

describe("PluginDisabledPage", () => {
  it("names the plugin by its id when no row exists", () => {
    renderApp(<PluginDisabledPage id="ghost" info={undefined} />);
    expect(screen.getByRole("status")).toHaveTextContent("This dash has no ghost plugin.");
  });
});
