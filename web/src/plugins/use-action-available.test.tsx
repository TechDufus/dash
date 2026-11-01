import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import type { ActionInfo, PluginInfo } from "@/api/types";
import {
  type FakeEventSource,
  installEventSource,
  pluginInfo,
  pluginsEnvelope,
  stubFetch,
} from "@/test/harness";
import { actionAvailable, useActionAvailable } from "./use-action-available";

const action = (name: string, minRole: string): ActionInfo => ({
  name,
  title: name,
  destructive: false,
  idempotent: true,
  confirm: "none",
  minRole,
  inputSchema: null,
});

const row = (actions: ActionInfo[], state = "healthy"): PluginInfo =>
  pluginInfo({ id: "alertmanager", state, actions });

describe("actionAvailable", () => {
  const restart = action("restart", "admin");
  const silence = action("silence", "viewer");

  it("is true when the row lists the action and the role meets its minRole", () => {
    expect(actionAvailable(row([restart]), "restart", "admin")).toBe(true);
    expect(actionAvailable(row([silence]), "silence", "viewer")).toBe(true);
    // Admin includes viewer.
    expect(actionAvailable(row([silence]), "silence", "admin")).toBe(true);
  });

  it("is false for a viewer facing an admin-only action", () => {
    expect(actionAvailable(row([restart]), "restart", "viewer")).toBe(false);
  });

  it("is false when the row does not list the action", () => {
    expect(actionAvailable(row([silence]), "restart", "admin")).toBe(false);
    expect(actionAvailable(row([]), "restart", "admin")).toBe(false);
  });

  it("is false when the row is missing or hidden", () => {
    expect(actionAvailable(undefined, "restart", "admin")).toBe(false);
    for (const state of ["disabled", "unavailable", "unconfigured", "blocked"]) {
      expect(actionAvailable(row([restart], state), "restart", "admin")).toBe(false);
    }
  });

  it("is false while the role is unknown, and offers nothing a newer role scheme added", () => {
    expect(actionAvailable(row([restart]), "restart", undefined)).toBe(false);
    expect(actionAvailable(row([restart]), "restart", "operator")).toBe(false);
    // An unknown minRole counts as admin-only.
    expect(actionAvailable(row([action("x", "root")]), "x", "viewer")).toBe(false);
    expect(actionAvailable(row([action("x", "root")]), "x", "admin")).toBe(true);
  });
});

describe("useActionAvailable", () => {
  let ES: typeof FakeEventSource;
  let es: FakeEventSource;

  const wrapper = ({ children }: { children: ReactNode }) => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  };

  beforeEach(() => {
    ES = installEventSource();
  });
  afterEach(() => stopStream());

  function connect() {
    startStream();
    const instance = ES.instances.at(-1);
    if (!instance) throw new Error("no EventSource");
    instance.open();
    es = instance;
  }

  it("follows the live core.plugins row without refetching /me", async () => {
    const stub = stubFetch({
      "GET /api/v1/me": { user: "dev", role: "admin", scopes: ["read"], ui: { iconBaseUrl: "" } },
    });
    connect();
    act(() => es.emit("core.plugins", pluginsEnvelope([row([])])));
    const { result } = renderHook(() => useActionAvailable("alertmanager", "restart"), { wrapper });
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    expect(result.current).toBe(false);

    // actions.enabled switched on: the row now lists the action.
    act(() => es.emit("core.plugins", pluginsEnvelope([row([action("restart", "admin")])])));
    await waitFor(() => expect(result.current).toBe(true));

    // And off again.
    act(() => es.emit("core.plugins", pluginsEnvelope([row([])])));
    await waitFor(() => expect(result.current).toBe(false));
    expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1);
  });

  it("is false for a viewer and while /me is loading", async () => {
    const stub = stubFetch({
      "GET /api/v1/me": { user: "v", role: "viewer", scopes: [], ui: { iconBaseUrl: "" } },
    });
    connect();
    act(() => es.emit("core.plugins", pluginsEnvelope([row([action("restart", "admin")])])));
    const { result } = renderHook(() => useActionAvailable("alertmanager", "restart"), { wrapper });
    expect(result.current).toBe(false);
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    await waitFor(() => expect(result.current).toBe(false));
  });

  it("is true for a viewer on a viewer action once /me has answered", async () => {
    stubFetch({
      "GET /api/v1/me": { user: "v", role: "viewer", scopes: [], ui: { iconBaseUrl: "" } },
    });
    connect();
    act(() => es.emit("core.plugins", pluginsEnvelope([row([action("silence", "viewer")])])));
    const { result } = renderHook(() => useActionAvailable("alertmanager", "silence"), { wrapper });
    expect(result.current).toBe(false);
    await waitFor(() => expect(result.current).toBe(true));
  });
});
