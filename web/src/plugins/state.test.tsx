import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, screen, waitFor, within } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { getEnvelope, startStream, stopStream } from "@/api/stream";
import type { PluginInfo, PluginsData } from "@/api/types";
import { Shell } from "@/shell/shell";
import {
  envelope,
  type FakeEventSource,
  installEventSource,
  pluginInfo,
  pluginsEnvelope,
  renderApp,
  stubFetch,
} from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
import { PluginRoutes } from "./routes";
import { isVisible, usePluginsState, usePluginVisible, useTopicAvailable } from "./state";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

let ES: typeof FakeEventSource;

beforeEach(() => {
  ES = installEventSource();
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

describe("isVisible", () => {
  it.each([
    ["starting", true],
    ["healthy", true],
    ["degraded", true],
    ["error", true],
    ["disabled", false],
    ["unavailable", false],
    ["unconfigured", false],
    ["blocked", false],
    ["a-state-from-a-newer-server", true],
  ])("%s -> %s", (state, visible) => {
    expect(isVisible(state)).toBe(visible);
  });
});

describe("usePluginsState", () => {
  it("answers from GET /api/v1/plugins until the stream has spoken, then the stream wins", async () => {
    const stub = stubFetch({
      "GET /api/v1/plugins": [pluginInfo({ id: "alpha", name: "from REST" })],
    });
    const { result } = renderHook(() => usePluginsState(), { wrapper });
    expect(result.current.ready).toBe(false);
    expect(result.current.plugins).toEqual([]);

    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.byId.get("alpha")?.name).toBe("from REST");

    const es = connect();
    act(() =>
      es.emit("core.plugins", pluginsEnvelope([pluginInfo({ id: "beta", name: "from stream" })])),
    );
    expect(result.current.plugins.map((p) => p.id)).toEqual(["beta"]);
    expect(result.current.byId.has("alpha")).toBe(false);
    expect(stub.callsTo("GET /api/v1/plugins")).toHaveLength(1);
  });

  it("is ready from the stream alone, without asking REST", () => {
    const stub = stubFetch();
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([pluginInfo({ id: "beta" })]));
    const { result } = renderHook(() => usePluginsState(), { wrapper });
    expect(result.current.ready).toBe(true);
    expect(stub.callsTo("GET /api/v1/plugins")).toHaveLength(0);
  });

  it("flips ready once: later events and a REST answer after the stream do not reset it", async () => {
    stubFetch({ "GET /api/v1/plugins": [pluginInfo({ id: "alpha" })] });
    const seen: boolean[] = [];
    const { result } = renderHook(
      () => {
        const s = usePluginsState();
        seen.push(s.ready);
        return s;
      },
      { wrapper },
    );
    await waitFor(() => expect(result.current.ready).toBe(true));
    const es = connect();
    act(() => es.emit("core.plugins", pluginsEnvelope([pluginInfo({ id: "beta" })])));
    act(() =>
      es.emit("core.plugins", pluginsEnvelope([pluginInfo({ id: "beta", state: "error" })])),
    );
    expect(seen.join()).toMatch(/^(false,)+(true,?)+$/);
  });

  it("keeps a stable state object while nothing changed", () => {
    stubFetch();
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([pluginInfo({ id: "beta" })]));
    const { result, rerender } = renderHook(() => usePluginsState(), { wrapper });
    const first = result.current;
    rerender();
    expect(result.current).toBe(first);
  });
});

/** The rows of a v0.5.0 server: the plugin list had no `generic` field yet. */
function legacyRows(): PluginInfo[] {
  return shimRows().map((row) => {
    const { generic: _generic, ...legacy } = row;
    return legacy as PluginInfo;
  });
}

describe("rows from a v0.5.0 server (rollback of an open tab)", () => {
  it("get an empty generic list from the stream", () => {
    stubFetch();
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope(legacyRows()));
    const { result } = renderHook(() => usePluginsState(), { wrapper });
    expect(result.current.plugins).toHaveLength(shimRows().length);
    expect(
      result.current.plugins.every((p) => Array.isArray(p.generic) && p.generic.length === 0),
    ).toBe(true);
    // The store the stream keeps is complete too, for anything that reads it without the hook.
    const stored = getEnvelope("core.plugins")?.data as PluginsData;
    expect(stored.plugins.every((p) => Array.isArray(p.generic))).toBe(true);
  });

  it("get an empty generic list from GET /api/v1/plugins", async () => {
    stubFetch({ "GET /api/v1/plugins": legacyRows() });
    const { result } = renderHook(() => usePluginsState(), { wrapper });
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.plugins.every((p) => Array.isArray(p.generic))).toBe(true);
  });

  it.each(["stream", "REST"])(
    "draw the rail and the pages without throwing (%s)",
    async (source) => {
      stubFetch({
        "GET /api/v1/me": { user: "dev", role: "admin", version: "test" },
        "GET /api/v1/plugins": source === "REST" ? legacyRows() : () => new Promise(() => {}),
      });
      const es = connect();
      if (source === "stream") {
        es.emit("core.plugins", pluginsEnvelope(legacyRows()));
        es.emit("status", envelope("status", { endpoints: [] }));
      }
      renderApp(
        <Shell>
          <PluginRoutes />
        </Shell>,
        { path: "/status" },
      );
      const rail = await screen.findByRole("navigation", { name: "Primary" });
      expect(await within(rail).findByRole("link", { name: "Status" })).toBeInTheDocument();
      expect(within(rail).getByRole("link", { name: "Cluster" })).toBeInTheDocument();
      if (source === "stream") {
        expect(await screen.findByText("No endpoints reported.")).toBeInTheDocument();
      }
    },
  );

  it("render NotFound at /x/<id>, which a plugin without views has no page at", async () => {
    stubFetch();
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope(legacyRows()));
    renderApp(<PluginRoutes />, { path: "/x/gatus" });
    expect(await screen.findByText("Nothing here. Pick a page from the rail.")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
  });
});

describe("usePluginVisible and useTopicAvailable", () => {
  it("are undefined while the list loads, then follow the plugin's state", () => {
    stubFetch();
    const es = connect();
    const { result } = renderHook(
      () => ({
        proxmox: usePluginVisible("proxmox"),
        ghost: usePluginVisible("ghost"),
        vms: useTopicAvailable("vms"),
        overview: useTopicAvailable("core.overview"),
      }),
      { wrapper },
    );
    expect(result.current).toEqual({
      proxmox: undefined,
      ghost: undefined,
      vms: undefined,
      overview: true,
    });

    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));
    expect(result.current).toEqual({ proxmox: true, ghost: false, vms: true, overview: true });

    act(() =>
      es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "proxmox", "disabled"))),
    );
    expect(result.current).toEqual({ proxmox: false, ghost: false, vms: false, overview: true });
  });
});
