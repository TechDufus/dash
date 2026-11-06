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

/** Lets every pending promise and the fetch stub's response parsing settle (a yield, not a wait). */
const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

/** A promise the test settles by hand (tsconfig's lib predates Promise.withResolvers). */
function deferred() {
  let release: (v: unknown) => void = () => {};
  const gate = new Promise<unknown>((r) => {
    release = r;
  });
  return { gate, release };
}

const vmsRow = (state = "healthy") => pluginInfo({ id: "proxmox", topics: ["vms"], state });
const fluxRow = (state = "healthy") => pluginInfo({ id: "flux", topics: ["flux.objects"], state });

beforeEach(() => {
  ES = installEventSource();
  fetchStub = stubFetch({
    "GET /api/v1/me": { user: "dev", role: "admin", ui: { iconBaseUrl: "" } },
  });
});

afterEach(() => {
  stopStream();
});

describe("stream.ts per-plugin topics", () => {
  it("listens to the core topics only until a plugin list arrives", () => {
    const es = connect();
    expect([...es.listenedTo].sort()).toEqual(["core.approvals", "core.overview", "core.plugins"]);
  });

  it("attaches a listener for every topic a core.plugins event lists, hidden plugins included", () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("disabled")]));
    expect([...es.listenedTo].sort()).toEqual([
      "core.approvals",
      "core.overview",
      "core.plugins",
      "flux.objects",
      "vms",
    ]);

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
    es.emit("flux.objects", envelope("flux.objects", { objects: [] }));

    es.emit("core.plugins", pluginsEnvelope([vmsRow("disabled"), fluxRow()]));

    expect(getEnvelope("vms")).toBeUndefined();
    expect(getEnvelope("flux.objects")).toBeDefined();
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
    fetchStub.route(topicRoute("flux.objects"), envelope("flux.objects", { objects: ["a"] }));
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));

    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("starting")]));
    await vi.waitFor(() => expect(getEnvelope("flux.objects")?.data).toEqual({ objects: ["a"] }));

    // The same row again does not fetch again, and the topic already known is not fetched at all.
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    expect(fetchStub.callsTo(topicRoute("flux.objects"))).toHaveLength(1);
    expect(fetchStub.callsTo(topicRoute("vms"))).toHaveLength(0);
  });

  it("does not fetch a topic it already listens to when its plugin is switched on again", async () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("vms", envelope("vms", { vms: ["old"] }));
    es.emit("core.plugins", pluginsEnvelope([vmsRow("disabled")]));
    expect(getEnvelope("vms")).toBeUndefined();

    // The hub forgot the topic on disable, so the plugin's first publish after the restart is sent.
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("vms", envelope("vms", { vms: ["back"] }));
    await flush();
    expect(getEnvelope("vms")?.data).toEqual({ vms: ["back"] });
    expect(fetchStub.callsTo(topicRoute("vms"))).toHaveLength(0);
  });

  it("shows a topic event resent right after the core.plugins event that made its row visible", async () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("unconfigured")]));
    expect(getEnvelope("flux.objects")).toBeUndefined();

    // The server re-publishes a plugin's latest events after the row turns visible (a restart or
    // an enable); the topic already had a listener, so nothing seeds it and nothing needs to.
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("starting")]));
    es.emit("flux.objects", envelope("flux.objects", { objects: ["resent"] }));
    await flush();

    expect(getEnvelope("flux.objects")?.data).toEqual({ objects: ["resent"] });
    expect(fetchStub.callsTo(topicRoute("flux.objects"))).toHaveLength(0);
  });

  it("keeps a topic's first publish when it arrives before the event that makes it visible", async () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("disabled")]));
    expect(getEnvelope("flux.objects")).toBeUndefined();

    es.emit("flux.objects", envelope("flux.objects", { objects: ["first"] }));
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("starting")]));
    await flush();

    expect(getEnvelope("flux.objects")?.data).toEqual({ objects: ["first"] });
    expect(fetchStub.callsTo(topicRoute("flux.objects"))).toHaveLength(0);
  });

  it("does not overwrite what the stream delivered while the fetch was running", async () => {
    const { gate, release } = deferred();
    fetchStub.route(topicRoute("flux.objects"), () => gate);
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    es.emit("flux.objects", envelope("flux.objects", { objects: ["from stream"] }));
    await vi.waitFor(() => expect(fetchStub.callsTo(topicRoute("flux.objects"))).toHaveLength(1));
    release(envelope("flux.objects", { objects: ["from fetch"] }));
    await flush();
    expect(getEnvelope("flux.objects")?.data).toEqual({ objects: ["from stream"] });
  });

  it("ignores a 404: the plugin has not published yet", async () => {
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    await vi.waitFor(() => expect(fetchStub.callsTo(topicRoute("flux.objects"))).toHaveLength(1));
    await flush();
    expect(getEnvelope("flux.objects")).toBeUndefined();
    es.emit("flux.objects", envelope("flux.objects", { objects: [] }));
    expect(getEnvelope("flux.objects")).toBeDefined();
  });

  it("drops a fetch that finishes after the connection was replaced", async () => {
    const { gate, release } = deferred();
    fetchStub.route(topicRoute("flux.objects"), () => gate);
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    await vi.waitFor(() => expect(fetchStub.callsTo(topicRoute("flux.objects"))).toHaveLength(1));

    // A new connection lists flux as visible too, so only the connection check can stop the fetch.
    stopStream();
    const next = connect();
    next.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    release(envelope("flux.objects", { objects: ["stale"] }));
    await flush();
    expect(getEnvelope("flux.objects")).toBeUndefined();
  });

  it("drops a fetch that finishes after its plugin turned hidden", async () => {
    const { gate, release } = deferred();
    fetchStub.route(topicRoute("flux.objects"), () => gate);
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope([vmsRow()]));
    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("starting")]));
    await vi.waitFor(() => expect(fetchStub.callsTo(topicRoute("flux.objects"))).toHaveLength(1));

    es.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow("disabled")]));
    release(envelope("flux.objects", { objects: ["hidden"] }));
    await flush();
    expect(getEnvelope("flux.objects")).toBeUndefined();
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
    expect([...next.listenedTo].sort()).toEqual([
      "core.approvals",
      "core.overview",
      "core.plugins",
    ]);

    next.open();
    next.emit("core.plugins", pluginsEnvelope([vmsRow(), fluxRow()]));
    expect([...next.listenedTo].sort()).toEqual([
      "core.approvals",
      "core.overview",
      "core.plugins",
      "flux.objects",
      "vms",
    ]);
    expect(fetchStub.calls.filter((c) => c.path.startsWith("/api/v1/topics/"))).toEqual([]);
  });
});
