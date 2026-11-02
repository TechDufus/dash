import { useQuery } from "@tanstack/react-query";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { Link, useLocation } from "wouter";
import { api } from "@/api/client";
import {
  envelope,
  FakeEventSource,
  type FetchCall,
  installEventSource,
  json,
  pluginInfo,
  pluginsEnvelope,
  renderApp,
  stubFetch,
} from "./harness";

describe("FakeEventSource", () => {
  it("replaces the global and records instances", () => {
    installEventSource();
    const es = new EventSource("/api/v1/stream");
    expect(FakeEventSource.instances).toEqual([es]);
    expect(EventSource.CLOSED).toBe(2);
    installEventSource();
    expect(FakeEventSource.instances).toEqual([]);
  });

  it("delivers named events to listeners and parses nothing itself", () => {
    installEventSource();
    const es = new EventSource("/x") as unknown as FakeEventSource;
    const got: string[] = [];
    es.addEventListener("vms", (ev) => got.push((ev as MessageEvent<string>).data));
    es.emit("vms", { a: 1 });
    es.emit("other", "ignored");
    expect(got).toEqual(['{"a":1}']);
    expect([...es.listenedTo]).toEqual(["vms"]);
  });

  it("emits open and error, and fail() is final unless retrying", () => {
    installEventSource();
    const es = new EventSource("/x") as unknown as FakeEventSource;
    const seen: string[] = [];
    es.onopen = () => seen.push("open");
    es.onerror = () => seen.push(`error:${es.readyState}`);
    es.open();
    es.fail({ retrying: true });
    es.fail();
    expect(seen).toEqual(["open", "error:0", "error:2"]);
    // A closed source delivers nothing more.
    const got: string[] = [];
    es.addEventListener("vms", () => got.push("x"));
    es.emit("vms", {});
    expect(got).toEqual([]);
  });
});

describe("stubFetch", () => {
  it("answers by method and path, records calls and decodes bodies", async () => {
    const stub = stubFetch({
      "GET /api/v1/plugins": [pluginInfo({ id: "proxmox" })],
      "POST /api/v1/echo": (call: FetchCall) => ({ got: call.body, q: call.query.get("n") }),
      "DELETE /api/v1/thing": undefined,
    });

    expect(await api.get<{ id: string }[]>("/api/v1/plugins")).toMatchObject([{ id: "proxmox" }]);
    expect(await api.post("/api/v1/echo?n=3", { a: 1 })).toEqual({ got: { a: 1 }, q: "3" });
    expect(await api.del("/api/v1/thing")).toBeUndefined();

    expect(stub.calls.map((c) => `${c.method} ${c.path}`)).toEqual([
      "GET /api/v1/plugins",
      "POST /api/v1/echo",
      "DELETE /api/v1/thing",
    ]);
    // client.ts marks writes for the CSRF check; the stub exposes the headers it was sent.
    expect(stub.callsTo("POST /api/v1/echo")[0]?.headers.get("X-Dash-Csrf")).toBe("1");
    expect(stub.callsTo("GET /api/v1/plugins")[0]?.headers.get("X-Dash-Csrf")).toBeNull();
  });

  it("answers unrouted requests with a 404 the client turns into an ApiError", async () => {
    const stub = stubFetch();
    await expect(api.get("/api/v1/missing")).rejects.toMatchObject({
      status: 404,
      message: expect.stringContaining("no route for GET /api/v1/missing"),
    });
    expect(stub.calls[0]?.matched).toBe(false);
  });

  it("passes a Response through and lets a route be added later", async () => {
    const stub = stubFetch({
      "GET /api/v1/a": json({ error: "nope", code: "forbidden" }, 403),
    });
    await expect(api.get("/api/v1/a")).rejects.toMatchObject({ status: 403, code: "forbidden" });
    stub.route("GET /api/v1/b", { ok: true });
    expect(await api.get("/api/v1/b")).toEqual({ ok: true });
  });
});

function Probe() {
  const [location] = useLocation();
  const plugins = useQuery({
    queryKey: ["plugins"],
    queryFn: () => api.get<{ id: string }[]>("/api/v1/plugins"),
  });
  return (
    <div>
      <p>at {location}</p>
      <p>{plugins.data ? plugins.data.map((p) => p.id).join(",") : "loading"}</p>
      <Link href="/vms">VMs</Link>
    </div>
  );
}

describe("renderApp", () => {
  it("provides a router at the given path and a QueryClient that reaches the fetch stub", async () => {
    stubFetch({ "GET /api/v1/plugins": [pluginInfo({ id: "proxmox" })] });
    const app = renderApp(<Probe />, { path: "/cluster" });

    expect(screen.getByText("at /cluster")).toBeInTheDocument();
    expect(await screen.findByText("proxmox")).toBeInTheDocument();
    expect(app.queryClient.getQueryData(["plugins"])).toHaveLength(1);
  });

  it("navigates on a link click (user-event) and through navigate()", async () => {
    stubFetch({ "GET /api/v1/plugins": [] });
    const app = renderApp(<Probe />);

    await userEvent.click(screen.getByRole("link", { name: "VMs" }));
    expect(screen.getByText("at /vms")).toBeInTheDocument();

    app.navigate("/gitops");
    await waitFor(() => expect(screen.getByText("at /gitops")).toBeInTheDocument());
    expect(app.history).toEqual(["/", "/vms", "/gitops"]);
  });

  it("does not retry failed queries, so an error shows at once", async () => {
    const stub = stubFetch({ "GET /api/v1/plugins": () => json({ error: "down" }, 500) });
    const app = renderApp(<Probe />);
    await waitFor(() => expect(app.queryClient.getQueryState(["plugins"])?.status).toBe("error"));
    expect(stub.callsTo("GET /api/v1/plugins")).toHaveLength(1);
  });
});

describe("builders", () => {
  it("pluginInfo fills the wire shape and takes overrides", () => {
    expect(pluginInfo({ id: "flux", state: "degraded", topics: ["flux.objects"] })).toMatchObject({
      id: "flux",
      type: "flux",
      name: "flux",
      state: "degraded",
      topics: ["flux.objects"],
      actions: [],
      nav: [],
    });
  });

  it("pluginsEnvelope wraps the list as core.plugins data", () => {
    const list = [pluginInfo({ id: "a" }), pluginInfo({ id: "b" })];
    expect(pluginsEnvelope(list)).toEqual(envelope("core.plugins", { plugins: list }));
  });
});

describe("setup", () => {
  it("stubs the browser APIs Radix, cmdk and Recharts need", () => {
    expect(window.matchMedia("(min-width: 1px)").matches).toBe(false);
    expect(typeof ResizeObserver).toBe("function");
    expect(() => document.createElement("div").scrollIntoView()).not.toThrow();
    expect(document.createElement("div").hasPointerCapture(1)).toBe(false);
  });
  it("stubs globals inside one test…", () => {
    stubFetch();
    installEventSource();
    expect(vi.isMockFunction(globalThis.fetch)).toBe(true);
    expect(typeof EventSource).toBe("function");
  });

  it("…and removes them before the next one", () => {
    expect(vi.isMockFunction(globalThis.fetch)).toBe(false);
    expect(typeof EventSource).toBe("undefined");
  });
});
