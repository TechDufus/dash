import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { envelope, type FakeEventSource, installEventSource, stubFetch } from "@/test/harness";
import { startStream, stopStream, useConnection, useTopic } from "./stream";

// jsdom has no PageTransitionEvent; the handlers read only `persisted`.
function pageEvent(type: "pagehide" | "pageshow", persisted: boolean) {
  window.dispatchEvent(Object.assign(new Event(type), { persisted }));
}

const overview = (n: number) => envelope("core.overview", { kpiList: [], attention: [], n });

/** The slots the server would count: connections that are not closed. */
const open = (ES: typeof FakeEventSource) =>
  ES.instances.filter((es) => es.readyState !== ES.CLOSED);

let ES: typeof FakeEventSource;

beforeEach(() => {
  ES = installEventSource();
  stubFetch({ "GET /api/v1/me": { user: "dev", role: "admin", ui: { iconBaseUrl: "" } } });
  vi.useFakeTimers();
});

afterEach(() => {
  stopStream();
  vi.useRealTimers();
});

function connect() {
  startStream();
  const es = ES.instances.at(-1);
  if (!es) throw new Error("no EventSource");
  act(() => es.open());
  return es;
}

describe("stream.ts in the back/forward cache", () => {
  it("closes the stream and releases its slot when the page is cached", () => {
    const es = connect();
    expect(open(ES)).toEqual([es]);

    act(() => pageEvent("pagehide", true));

    expect(es.readyState).toBe(ES.CLOSED);
    expect(open(ES)).toHaveLength(0);
  });

  it("does not retry a stream that was failing when the page was cached", () => {
    startStream();
    const first = ES.instances[0];
    if (!first) throw new Error("no EventSource");
    act(() => first.fail()); // closed: the retry timer is pending
    act(() => pageEvent("pagehide", true));

    act(() => vi.advanceTimersByTime(10_000));

    expect(ES.instances).toHaveLength(1);
    expect(open(ES)).toHaveLength(0);
  });

  it("reopens one stream when the page comes back, and the state returns to live", () => {
    const first = connect();
    const { result } = renderHook(() => ({
      state: useConnection(),
      topic: useTopic("core.overview"),
    }));
    act(() => first.emit("core.overview", overview(1)));
    expect(result.current.state).toBe("live");

    act(() => pageEvent("pagehide", true));
    act(() => pageEvent("pageshow", true));

    expect(ES.instances).toHaveLength(2);
    const second = ES.instances[1];
    expect(second?.url).toBe("/api/v1/stream");
    expect(open(ES)).toEqual([second]);
    expect(result.current.state).toBe("reconnecting");
    // What the page showed stays on screen until the new stream's snapshot replaces it.
    expect(result.current.topic?.data).toEqual(overview(1).data);

    act(() => second?.open());
    expect(result.current.state).toBe("live");

    // A second pageshow (or one that did not follow a hide) opens nothing more.
    act(() => pageEvent("pageshow", true));
    expect(ES.instances).toHaveLength(2);
  });

  it("changes nothing when the page is hidden without being cached", () => {
    const es = connect();

    act(() => pageEvent("pagehide", false));
    act(() => pageEvent("pageshow", false));
    act(() => pageEvent("pageshow", true));

    expect(es.readyState).toBe(ES.OPEN);
    expect(ES.instances).toEqual([es]);
  });

  it("stays idempotent: repeat starts open one stream and register one pair of handlers", () => {
    const first = connect();
    startStream();
    startStream();
    expect(ES.instances).toEqual([first]);

    act(() => pageEvent("pagehide", true));
    act(() => pageEvent("pageshow", true));

    expect(ES.instances).toHaveLength(2);
    expect(open(ES)).toHaveLength(1);
  });

  it("does not reopen after stopStream", () => {
    connect();
    act(() => pageEvent("pagehide", true));
    stopStream();

    act(() => pageEvent("pageshow", true));

    expect(open(ES)).toHaveLength(0);
  });

  it("removes its own pagehide and pageshow handlers when it stops", () => {
    const added = vi.spyOn(window, "addEventListener");
    const removed = vi.spyOn(window, "removeEventListener");
    try {
      connect();
      const handlers = (type: string, calls: typeof added.mock.calls) =>
        calls.filter(([t]) => t === type).map(([, fn]) => fn);
      const [hide] = handlers("pagehide", added.mock.calls);
      const [show] = handlers("pageshow", added.mock.calls);
      expect(hide).toBeTypeOf("function");
      expect(show).toBeTypeOf("function");

      stopStream();

      expect(handlers("pagehide", removed.mock.calls)).toContain(hide);
      expect(handlers("pageshow", removed.mock.calls)).toContain(show);
    } finally {
      added.mockRestore();
      removed.mockRestore();
    }
  });

  it("does not open a stream for a page that never started one", () => {
    act(() => pageEvent("pagehide", true));
    act(() => pageEvent("pageshow", true));
    expect(ES.instances).toHaveLength(0);
  });
});

describe("stream.ts after a cached page's stream closed", () => {
  it("reopens only for a pageshow that restores the page from the cache", () => {
    connect();
    act(() => pageEvent("pagehide", true));

    act(() => pageEvent("pageshow", false));
    expect(open(ES)).toHaveLength(0);

    act(() => pageEvent("pageshow", true));
    expect(open(ES)).toHaveLength(1);
  });
});
