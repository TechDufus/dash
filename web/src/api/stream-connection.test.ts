import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  envelope,
  type FakeEventSource,
  type FetchStub,
  installEventSource,
  stubFetch,
} from "@/test/harness";

// stream.ts keeps one EventSource and its state at module level, so each test loads a fresh copy.
async function freshStream() {
  vi.resetModules();
  return import("./stream");
}

const overview = (n: number) => envelope("core.overview", { kpis: [], attention: [], n });

describe("stream.ts connection handling", () => {
  let ES: typeof FakeEventSource;
  let fetchStub: FetchStub;

  beforeEach(() => {
    ES = installEventSource();
    fetchStub = stubFetch({
      "GET /api/v1/me": { user: "dev", role: "admin", ui: { iconBaseUrl: "" } },
    });
    vi.useFakeTimers();
  });

  it("opens one EventSource on /api/v1/stream, however often startStream runs", async () => {
    const { startStream } = await freshStream();
    startStream();
    startStream();
    expect(ES.instances).toHaveLength(1);
    expect(ES.instances[0]?.url).toBe("/api/v1/stream");
  });

  it("stores a topic's envelope and goes live when it arrives", async () => {
    const { startStream, useConnection, useTopic } = await freshStream();
    startStream();
    const es = ES.instances[0];
    if (!es) throw new Error("no EventSource");

    const { result } = renderHook(() => ({
      state: useConnection(),
      topic: useTopic("core.overview"),
    }));
    expect(result.current.state).toBe("connecting");
    expect(result.current.topic).toBeUndefined();

    act(() => es.emit("core.overview", overview(1)));
    expect(result.current.state).toBe("live");
    expect(result.current.topic?.data).toEqual(overview(1).data);

    // A newer event replaces the stored one.
    act(() => es.emit("core.overview", overview(2)));
    expect(result.current.topic?.data).toEqual(overview(2).data);
  });

  it("keeps the last good envelope when an event is malformed", async () => {
    const { startStream, useTopic } = await freshStream();
    startStream();
    const es = ES.instances[0];
    if (!es) throw new Error("no EventSource");
    const { result } = renderHook(() => useTopic("core.overview"));

    act(() => es.emit("core.overview", overview(1)));
    act(() => es.emit("core.overview", "{not json"));
    expect(result.current?.data).toEqual(overview(1).data);
  });

  it("lets the browser retry a dropped connection and shows it as reconnecting", async () => {
    const { startStream, useConnection } = await freshStream();
    startStream();
    const es = ES.instances[0];
    if (!es) throw new Error("no EventSource");
    const { result } = renderHook(() => useConnection());

    act(() => es.emit("core.overview", overview(1)));
    act(() => es.fail({ retrying: true }));
    expect(result.current).toBe("reconnecting");

    act(() => vi.advanceTimersByTime(10_000));
    expect(ES.instances).toHaveLength(1);
    expect(fetchStub.callsTo("GET /api/v1/me")).toHaveLength(0);
  });

  it("opens a new EventSource 3 s after a closed one and probes /api/v1/me", async () => {
    const { startStream, useConnection, useTopic } = await freshStream();
    startStream();
    const first = ES.instances[0];
    if (!first) throw new Error("no EventSource");
    const { result } = renderHook(() => ({
      state: useConnection(),
      topic: useTopic("core.overview"),
    }));

    act(() => first.emit("core.overview", overview(1)));
    act(() => first.fail());
    expect(result.current.state).toBe("reconnecting");
    // The cached envelope stays on screen while the stream is down.
    expect(result.current.topic?.data).toEqual(overview(1).data);
    expect(fetchStub.callsTo("GET /api/v1/me")).toHaveLength(1);

    act(() => vi.advanceTimersByTime(2_999));
    expect(ES.instances).toHaveLength(1);
    act(() => vi.advanceTimersByTime(1));
    expect(ES.instances).toHaveLength(2);
    const second = ES.instances[1];
    expect(first.readyState).toBe(ES.CLOSED);
    expect(second?.url).toBe("/api/v1/stream");

    // The new connection delivers the snapshot again and the state returns to live.
    act(() => second?.open());
    expect(result.current.state).toBe("live");
  });

  it("stays 'connecting' while the first connection has not delivered anything", async () => {
    const { startStream, useConnection } = await freshStream();
    startStream();
    const es = ES.instances[0];
    if (!es) throw new Error("no EventSource");
    const { result } = renderHook(() => useConnection());
    act(() => es.fail({ retrying: true }));
    expect(result.current).toBe("connecting");
  });
});
