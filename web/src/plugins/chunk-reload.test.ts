import { afterEach, beforeEach, describe, expect, it, type Mock, vi } from "vitest";
import { installChunkReload } from "./chunk-reload";

function preloadError(): Event {
  const ev = new Event("vite:preloadError", { cancelable: true });
  window.dispatchEvent(ev);
  return ev;
}

describe("vite:preloadError", () => {
  let reload: Mock<() => void>;
  let uninstall: () => void;

  beforeEach(() => {
    vi.useFakeTimers();
    reload = vi.fn<() => void>();
    uninstall = installChunkReload(reload);
  });

  afterEach(() => {
    uninstall();
  });

  it("reloads once for a chunk that failed to load and keeps Vite from throwing", () => {
    const ev = preloadError();
    expect(ev.defaultPrevented).toBe(true);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("does not loop: a second failure within 30 s does not reload again", () => {
    preloadError();
    vi.advanceTimersByTime(29_000);
    preloadError();
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it("reloads again once the guard window has passed", () => {
    preloadError();
    vi.advanceTimersByTime(30_001);
    preloadError();
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it("stops listening when uninstalled", () => {
    uninstall();
    preloadError();
    expect(reload).not.toHaveBeenCalled();
  });
});
