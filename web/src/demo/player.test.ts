import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, api } from "@/api/client";
import { getEnvelope, startStream, stopStream } from "@/api/stream";
import { setTransport } from "@/api/transport";
import { RecordedClock } from "./clock";
import { Player, READ_ONLY_CODE, READ_ONLY_MESSAGE, ReplaySource } from "./player";
import { checkManifest, type Manifest, recordingKey } from "./recording";

const T0 = Date.parse("2026-01-15T12:00:00Z");

const plugins = (topics: string[]) =>
  JSON.stringify({
    topic: "core.plugins",
    updatedAt: "2026-01-15T12:00:00Z",
    sources: [],
    data: {
      plugins: [
        {
          id: "queue",
          type: "queue",
          name: "Queue",
          version: "1.0.0",
          state: "healthy",
          message: "",
          topics,
          actions: [],
          nav: [],
          generic: [],
          configurable: false,
          objects: false,
        },
      ],
    },
  });
const env = (topic: string, n: number) =>
  JSON.stringify({ topic, updatedAt: "2026-01-15T12:00:00Z", sources: [], data: { n } });

/** Three frames 5 s apart: queue.a changes in frame 1, nothing changes in frame 2. */
const FILES: Record<string, string> = {
  "plugins.json": plugins(["queue.a", "queue.b"]),
  "a1.json": env("queue.a", 1),
  "a2.json": env("queue.a", 2),
  "b1.json": env("queue.b", 1),
  "pods.json": JSON.stringify([{ name: "web" }]),
  "notes.txt": "# notes",
};
const MANIFEST: Manifest = {
  version: 1,
  stepSeconds: 5,
  frames: [
    {
      at: "2026-01-15T12:00:00Z",
      topics: { "queue.a": "a1.json", "core.plugins": "plugins.json", "queue.b": "b1.json" },
    },
    {
      at: "2026-01-15T12:00:05Z",
      topics: { "core.plugins": "plugins.json", "queue.a": "a2.json", "queue.b": "b1.json" },
    },
    {
      at: "2026-01-15T12:00:10Z",
      topics: { "core.plugins": "plugins.json", "queue.a": "a2.json", "queue.b": "b1.json" },
    },
  ],
  rest: {
    "/api/v1/pods?a=1&b=2": "pods.json",
    "/api/v1/notes": "notes.txt",
  },
};

function player(elapsed = () => 0) {
  const misses: string[] = [];
  const loads: string[] = [];
  const clock = new RecordedClock(0, elapsed);
  const p = new Player({
    manifest: MANIFEST,
    load: async (file) => {
      loads.push(file);
      const text = FILES[file];
      if (text === undefined) throw new Error(`no ${file}`);
      return text;
    },
    clock,
    onMiss: (key) => misses.push(key),
  });
  return { p, misses, loads, clock };
}

/** Every named event an EventSource gets, as [name, data.n or the topic list]. */
function record(es: EventSource, names: string[]) {
  const seen: [string, unknown][] = [];
  for (const n of names) {
    es.addEventListener(n, (ev) => {
      const data = JSON.parse((ev as MessageEvent<string>).data).data;
      seen.push([n, n === "core.plugins" ? data.plugins[0].topics : data.n]);
    });
  }
  return seen;
}

afterEach(() => {
  stopStream();
  setTransport({ fetch: (i, n) => fetch(i, n), eventSource: (u) => new EventSource(u) });
});

describe("recordingKey", () => {
  it("sorts the query by name, then value, so parameter order never matters", () => {
    expect(recordingKey("/api/v1/series", "range=1h&name=cpu&pod=b&pod=a")).toBe(
      "/api/v1/series?name=cpu&pod=a&pod=b&range=1h",
    );
    expect(
      recordingKey("/api/v1/series", new URLSearchParams("pod=a&name=cpu&range=1h&pod=b")),
    ).toBe("/api/v1/series?name=cpu&pod=a&pod=b&range=1h");
  });

  it("is the bare path without a query, and keeps encoding stable", () => {
    expect(recordingKey("/api/v1/me", "")).toBe("/api/v1/me");
    expect(recordingKey("/api/v1/logs", "q=a b&ns=x%2Fy")).toBe("/api/v1/logs?ns=x%2Fy&q=a+b");
  });
});

describe("checkManifest", () => {
  it("accepts a version 1 manifest and refuses anything else", () => {
    expect(checkManifest(MANIFEST)).toBe(MANIFEST);
    expect(() => checkManifest({ ...MANIFEST, version: 2 })).toThrow(/unknown format/);
    expect(() => checkManifest({ ...MANIFEST, frames: [] })).toThrow(/unknown format/);
    expect(() => checkManifest(null)).toThrow(/unknown format/);
  });
});

describe("Player fetch", () => {
  it("answers a recorded GET whatever the order of its query", async () => {
    const { p, misses } = player();
    const res = await p.fetch("/api/v1/pods?b=2&a=1");
    expect(res.status).toBe(200);
    expect(res.headers.get("Content-Type")).toBe("application/json");
    expect(await res.json()).toEqual([{ name: "web" }]);
    const text = await p.fetch("/api/v1/notes", { method: "GET" });
    expect(text.headers.get("Content-Type")).toMatch(/^text\/plain/);
    expect(await text.text()).toBe("# notes");
    expect(misses).toEqual([]);
  });

  it("answers a GET the recording lacks with the API's 404 envelope and counts it", async () => {
    const { p, misses } = player();
    const res = await p.fetch("/api/v1/pods?a=1");
    expect(res.status).toBe(404);
    expect(await res.json()).toEqual({
      error: "not found",
      code: "not_found",
      requestId: "recorded-demo",
    });
    expect(misses).toEqual(["/api/v1/pods?a=1"]);
  });

  it("answers a topic GET from the frame on screen", async () => {
    const { p, misses } = player();
    const res = await p.fetch("/api/v1/topics/queue.a");
    expect(JSON.parse(await res.text()).data.n).toBe(1);
    expect(misses).toEqual([]);
  });

  it("answers every write with the read-only message, which the client throws as is", async () => {
    const { p, misses } = player();
    for (const method of ["POST", "PUT", "PATCH", "DELETE"]) {
      const res = await p.fetch("/api/v1/plugins/kubernetes/actions/restart", { method });
      expect(res.status).toBe(405);
      expect(await res.json()).toEqual({
        error: READ_ONLY_MESSAGE,
        code: READ_ONLY_CODE,
        requestId: "recorded-demo",
      });
    }
    expect(misses).toEqual([]);

    setTransport(p);
    const err = await api.post("/api/v1/settings/import", {}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 405, code: READ_ONLY_CODE, message: READ_ONLY_MESSAGE });
    expect(READ_ONLY_MESSAGE).toContain(
      "docker run -p 8080:8080 -e DASH_DEMO=true -e DASH_SERVER_LISTEN=0.0.0.0:8080 ghcr.io/techdufus/dash",
    );
  });
});

describe("Player stream", () => {
  it("opens with every topic of the first frame, core.plugins first", async () => {
    vi.useFakeTimers();
    const { p } = player();
    const es = p.eventSource("/api/v1/stream");
    const seen = record(es, ["core.plugins", "queue.a", "queue.b"]);
    const onopen = vi.fn();
    es.onopen = onopen;
    await vi.advanceTimersByTimeAsync(0);
    expect(es.readyState).toBe(ReplaySource.OPEN);
    expect(onopen).toHaveBeenCalledOnce();
    expect(seen).toEqual([
      ["core.plugins", ["queue.a", "queue.b"]],
      ["queue.a", 1],
      ["queue.b", 1],
    ]);
  });

  it("sends only what changed each step, loops, and moves the clock with the frame", async () => {
    vi.useFakeTimers();
    let elapsed = 0;
    const { p, clock } = player(() => elapsed);
    const es = p.eventSource("/api/v1/stream");
    const seen = record(es, ["core.plugins", "queue.a", "queue.b"]);
    await vi.advanceTimersByTimeAsync(0);
    expect(clock.now()).toBe(T0);
    seen.length = 0;

    elapsed = 5000;
    await vi.advanceTimersByTimeAsync(5000);
    expect(seen).toEqual([["queue.a", 2]]);
    expect(clock.now()).toBe(T0 + 5000);

    elapsed = 10_000;
    await vi.advanceTimersByTimeAsync(5000);
    expect(seen).toEqual([["queue.a", 2]]); // frame 2 changed nothing
    expect(clock.now()).toBe(T0 + 10_000);

    elapsed = 15_000;
    await vi.advanceTimersByTimeAsync(5000);
    // Back to frame 0: queue.a differs from what was sent last, the rest does not.
    expect(seen).toEqual([
      ["queue.a", 2],
      ["queue.a", 1],
    ]);
    expect(clock.now()).toBe(T0);

    es.close();
    await vi.advanceTimersByTimeAsync(20_000);
    expect(seen).toHaveLength(2);
  });

  it("loads frames lazily: the first frame, then each one a step ahead", async () => {
    vi.useFakeTimers();
    const { p, loads } = player();
    expect(loads).toEqual([]);
    p.eventSource("/api/v1/stream");
    await vi.advanceTimersByTimeAsync(0);
    expect(new Set(loads)).toEqual(new Set(["plugins.json", "a1.json", "b1.json", "a2.json"]));
  });

  it("fails any other event stream as a closed one, like the log tail without a server", async () => {
    vi.useFakeTimers();
    const { p } = player();
    const es = p.eventSource("/api/v1/plugins/victorialogs/logs/tail?ns=a&pod=b");
    const onerror = vi.fn();
    es.onerror = onerror;
    await vi.advanceTimersByTimeAsync(0);
    expect(onerror).toHaveBeenCalledOnce();
    // EventSource.CLOSED: what pod-logs.tsx compares readyState with.
    expect(es.readyState).toBe(2);
  });

  it("drives the app's stream store: live, with the recorded envelopes", async () => {
    vi.useFakeTimers();
    const { p } = player();
    setTransport(p);
    startStream();
    await vi.advanceTimersByTimeAsync(0);
    expect(getEnvelope("queue.a")?.data).toEqual({ n: 1 });
    expect(getEnvelope("queue.b")?.data).toEqual({ n: 1 });
    await vi.advanceTimersByTimeAsync(5000);
    expect(getEnvelope("queue.a")?.data).toEqual({ n: 2 });
  });
});
