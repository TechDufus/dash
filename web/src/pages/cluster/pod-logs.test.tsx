import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FakeEventSource, installEventSource, renderApp, stubFetch } from "@/test/harness";
import { PodLogs } from "./pod-logs";

// jsdom has no PageTransitionEvent; the handlers read only `persisted`.
function pageEvent(type: "pagehide" | "pageshow", persisted: boolean) {
  window.dispatchEvent(Object.assign(new Event(type), { persisted }));
}

const tails = () => FakeEventSource.instances.filter((es) => es.url.includes("/logs/tail"));
const openTails = () => tails().filter((es) => es.readyState !== FakeEventSource.CLOSED);

const lineData = (msg: string) => ({ time: "2026-01-01T00:00:00Z", pod: "web-0", msg });

async function goLive() {
  const user = userEvent.setup();
  renderApp(<PodLogs ns="media" pod="web-0" containers={[]} active />);
  await user.click(await screen.findByRole("button", { name: "Live" }));
  const tail = tails()[0];
  if (!tail) throw new Error("no tail");
  return tail;
}

describe("PodLogs live tail in the back/forward cache", () => {
  beforeEach(() => {
    installEventSource();
    stubFetch({ "GET /api/v1/plugins/victorialogs/logs": [] });
  });
  afterEach(() => vi.useRealTimers());

  it("closes the tail and releases its slot when the page is cached", async () => {
    const tail = await goLive();
    act(() => tail.open());
    expect(openTails()).toEqual([tail]);

    act(() => pageEvent("pagehide", true));

    expect(tail.readyState).toBe(FakeEventSource.CLOSED);
    expect(openTails()).toHaveLength(0);
  });

  it("reopens one tail when the page comes back, and the state returns to streaming", async () => {
    const first = await goLive();
    act(() => first.open());
    // Still in the 100 ms batch when the page is cached: caching must not drop it.
    act(() => first.emit("line", lineData("before")));

    act(() => pageEvent("pagehide", true));
    act(() => pageEvent("pageshow", true));

    expect(tails()).toHaveLength(2);
    const second = tails()[1];
    expect(second?.url).toBe(first.url);
    expect(screen.getByText("connecting…")).toBeInTheDocument();

    act(() => second?.open());
    await waitFor(() => expect(screen.getByText("streaming")).toBeInTheDocument());
    // The lines shown before stay; the new tail adds to them.
    expect(screen.getByText("before")).toBeInTheDocument();
    act(() => second?.emit("line", lineData("after")));
    await screen.findByText("after", {}, { timeout: 2000 });
    expect(screen.getByText("before")).toBeInTheDocument();

    // A repeat pageshow opens nothing more.
    act(() => pageEvent("pageshow", true));
    expect(tails()).toHaveLength(2);
  });

  it("changes nothing when the page is hidden without being cached", async () => {
    const tail = await goLive();
    act(() => tail.open());

    act(() => pageEvent("pagehide", false));
    act(() => pageEvent("pageshow", false));
    act(() => pageEvent("pageshow", true));

    expect(tail.readyState).toBe(FakeEventSource.OPEN);
    expect(tails()).toEqual([tail]);
  });

  it("starts nothing on pageshow when the tail was never on", async () => {
    renderApp(<PodLogs ns="media" pod="web-0" containers={[]} active />);
    await screen.findByRole("button", { name: "Live" });

    act(() => pageEvent("pagehide", true));
    act(() => pageEvent("pageshow", true));

    expect(tails()).toHaveLength(0);
  });

  it("leaves no handler behind when the tail is switched off", async () => {
    const user = userEvent.setup();
    const tail = await goLive();
    await user.click(screen.getByRole("button", { name: "Live" }));
    expect(tail.readyState).toBe(FakeEventSource.CLOSED);

    act(() => pageEvent("pagehide", true));
    act(() => pageEvent("pageshow", true));

    expect(tails()).toHaveLength(1);
  });
});

describe("PodLogs live tail after a cached page's tail closed", () => {
  beforeEach(() => {
    installEventSource();
    stubFetch({ "GET /api/v1/plugins/victorialogs/logs": [] });
  });

  it("reopens only for a pageshow that restores the page from the cache", async () => {
    const tail = await goLive();
    act(() => tail.open());
    act(() => pageEvent("pagehide", true));

    act(() => pageEvent("pageshow", false));
    expect(openTails()).toHaveLength(0);

    act(() => pageEvent("pageshow", true));
    expect(openTails()).toHaveLength(1);
  });
});
