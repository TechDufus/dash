import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import {
  type FakeEventSource,
  installEventSource,
  pluginInfo,
  pluginsEnvelope,
  stubFetch,
} from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
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
