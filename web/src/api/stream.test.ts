import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  envelope,
  type FakeEventSource,
  type FetchStub,
  installEventSource,
  pluginInfo,
  pluginsEnvelope,
  stubFetch,
} from "@/test/harness";
import { getEnvelope, startStream, stopStream } from "./stream";

let ES: typeof FakeEventSource;
let fetchStub: FetchStub;

function connect(): FakeEventSource {
  startStream();
  const es = ES.instances.at(-1);
  if (!es) throw new Error("no EventSource");
  es.open();
  return es;
}

const vmsRow = (state = "healthy") => pluginInfo({ id: "proxmox", topics: ["vms"], state });
const fluxRow = (state = "healthy") => pluginInfo({ id: "flux", topics: ["flux"], state });

beforeEach(() => {
  ES = installEventSource();
  fetchStub = stubFetch({ "GET /api/v1/me": { user: "dev", role: "admin" } });
});

afterEach(() => {
  stopStream();
});

describe("stream.ts per-plugin topics", () => {
  it("listens to core.plugins and core.overview only until a plugin list arrives", () => {
    const es = connect();
    expect([...es.listenedTo].sort()).toEqual(["core.overview", "core.plugins"]);
  });

  it("attaches a listener for every topic a core.plugins event lists, hidden plugins included", () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("disabled")]));
    expect([...es.listenedTo].sort()).toEqual(["core.overview", "core.plugins", "flux", "vms"]);

    es.emit("vms", envelope("vms", { vms: [] }));
    expect(getEnvelope("vms")?.data).toEqual({ vms: [] });
  });

  it("attaches each listener once, however often core.plugins repeats", () => {
    const es = connect();
    const add = vi.spyOn(es, "addEventListener");
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("core.plugins", pluginsEnvelope([vmsRow("degraded")]));
    expect(add.mock.calls.filter(([name]) => name === "vms")).toHaveLength(1);
  });

  it("drops the envelope of a topic when its plugin turns disabled, and keeps core.*", () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    es.emit("core.overview", envelope("core.overview", { n: 1 }));
    es.emit("vms", envelope("vms", { vms: [] }));
    es.emit("flux", envelope("flux", { objects: [] }));

    es.emit("core.plugins", pluginsEnvelope([vmsRow("disabled"), fluxRow()]));

    expect(getEnvelope("vms")).toBeUndefined();
    expect(getEnvelope("flux")).toBeDefined();
    expect(getEnvelope("core.overview")).toBeDefined();
    expect(getEnvelope("core.plugins")).toBeDefined();
  });

  it.each(["unavailable", "unconfigured", "blocked", "disabled"])(
    "drops the envelope of a plugin that is %s",
    (state) => {
      const es = connect();
      es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
      es.emit("vms", envelope("vms", {}));
      es.emit("core.plugins", pluginsEnvelope([vmsRow(state)]));
      expect(getEnvelope("vms")).toBeUndefined();
    },
  );

  it.each(["starting", "healthy", "degraded", "error"])(
    "keeps the envelope of a plugin that is %s",
    (state) => {
      const es = connect();
      es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
      es.emit("vms", envelope("vms", {}));
      es.emit("core.plugins", pluginsEnvelope([vmsRow(state)]));
      expect(getEnvelope("vms")).toBeDefined();
    },
  );

  it("drops the envelope of a topic that no plugin lists any more", () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("vms", envelope("vms", {}));
    es.emit("core.plugins", pluginsEnvelope([]));
    expect(getEnvelope("vms")).toBeUndefined();
  });

  it("ignores a malformed core.plugins event and keeps the last good list", () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("vms", envelope("vms", {}));
    es.emit("core.plugins", "{not json");
    es.emit("core.plugins", envelope("core.plugins", { plugins: "nope" }));
    es.emit("core.plugins", "null");
    expect(getEnvelope("core.plugins")).toEqual(pluginsEnvelope([vmsRow()]));
    expect(getEnvelope("vms")).toBeDefined();
  });
});

describe("stream.ts snapshot seeding", () => {
  const topicRoute = (name: string) => `GET /api/v1/topics/${name}`;

  it("seeds nothing on connect: the snapshot follows core.plugins on the same stream", () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    expect(fetchStub.calls.filter((c) => c.path.startsWith("/api/v1/topics/"))).toEqual([]);
  });

  it("fetches a topic once when a plugin appears in a later core.plugins event", async () => {
    fetchStub.route(topicRoute("flux"), envelope("flux", { objects: ["a"] }));
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));

    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("starting")]));
    await vi.waitFor(() => expect(getEnvelope("flux")?.data).toEqual({ objects: ["a"] }));

    // The same row again does not fetch again, and the topic already known is not fetched at all.
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    expect(fetchStub.callsTo(topicRoute("flux"))).toHaveLength(1);
    expect(fetchStub.callsTo(topicRoute("vms"))).toHaveLength(0);
  });

  it("seeds a plugin that was switched off and on again", async () => {
    fetchStub.route(topicRoute("vms"), envelope("vms", { vms: ["back"] }));
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("vms", envelope("vms", { vms: ["old"] }));
    es.emit("core.plugins", pluginsEnvelope([vmsRow("disabled")]));
    expect(getEnvelope("vms")).toBeUndefined();

    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    await vi.waitFor(() => expect(getEnvelope("vms")?.data).toEqual({ vms: ["back"] }));
  });

  it("does not overwrite what the stream delivered while the fetch was running", async () => {
    // tsconfig's lib predates Promise.withResolvers
    let release: (v: unknown) => void = () => {};
    const gate = new Promise<unknown>((r) => {
      release = r;
    });
    fetchStub.route(topicRoute("flux"), () => gate);
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    es.emit("flux", envelope("flux", { objects: ["from stream"] }));
    release(envelope("flux", { objects: ["from fetch"] }));
    await vi.waitFor(() => expect(fetchStub.callsTo(topicRoute("flux"))).toHaveLength(1));
    await Promise.resolve();
    expect(getEnvelope("flux")?.data).toEqual({ objects: ["from stream"] });
  });

  it("ignores a 404: the plugin has not published yet", async () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    await vi.waitFor(() => expect(fetchStub.callsTo(topicRoute("flux"))).toHaveLength(1));
    expect(getEnvelope("flux")).toBeUndefined();
    es.emit("flux", envelope("flux", { objects: [] }));
    expect(getEnvelope("flux")).toBeDefined();
  });

  it("starts over after a reconnect: boot listeners again, and no seeding on the new connect", async () => {
    vi.useFakeTimers();
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.fail();
    await vi.advanceTimersByTimeAsync(3000);

    const next = ES.instances[1];
    expect(next).toBeDefined();
    if (!next) return;
    expect([...next.listenedTo].sort()).toEqual(["core.overview", "core.plugins"]);

    next.open();
    next.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    expect([...next.listenedTo].sort()).toEqual(["core.overview", "core.plugins", "flux", "vms"]);
    expect(fetchStub.calls.filter((c) => c.path.startsWith("/api/v1/topics/"))).toEqual([]);
  });
});
