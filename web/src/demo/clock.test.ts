import { afterEach, describe, expect, it } from "vitest";
import { installClock, RecordedClock } from "./clock";

const T0 = Date.parse("2026-01-15T12:00:00Z");
let uninstall: (() => void) | undefined;

afterEach(() => {
  uninstall?.();
  uninstall = undefined;
});

describe("RecordedClock", () => {
  it("is the frame's instant plus the real time since it was shown", () => {
    let elapsed = 1000;
    const clock = new RecordedClock(T0, () => elapsed);
    expect(clock.now()).toBe(T0);
    elapsed = 3500;
    expect(clock.now()).toBe(T0 + 2500);
    clock.show(T0 + 5000);
    expect(clock.now()).toBe(T0 + 5000);
    elapsed = 4000;
    expect(clock.now()).toBe(T0 + 5500);
    // The loop goes back to the first frame: so does the clock.
    clock.show(T0);
    expect(clock.now()).toBe(T0);
  });
});

describe("installClock", () => {
  it("moves Date.now() and new Date() only", () => {
    const RealDate = Date;
    let now = T0;
    uninstall = installClock(() => now);
    expect(Date.now()).toBe(T0);
    expect(new Date().toISOString()).toBe("2026-01-15T12:00:00.000Z");
    now = T0 + 60_000;
    expect(new Date().toISOString()).toBe("2026-01-15T12:01:00.000Z");
    // A Date of a given time, parse and UTC are the browser's.
    expect(new Date(0).getTime()).toBe(0);
    expect(new Date("2020-01-02T03:04:05Z").getTime()).toBe(RealDate.UTC(2020, 0, 2, 3, 4, 5));
    expect(new Date(2020, 0, 2).getFullYear()).toBe(2020);
    expect(Date.parse("2020-01-01T00:00:00Z")).toBe(1577836800000);
    expect(Date.UTC(2020, 0, 1)).toBe(1577836800000);
    expect(new Date()).toBeInstanceOf(Date);
    expect(new Date()).toBeInstanceOf(RealDate);
    expect(typeof Date()).toBe("string");
    expect(Date()).toBe(new RealDate(T0 + 60_000).toString());
  });

  it("puts the browser's Date back", () => {
    const RealDate = Date;
    uninstall = installClock(() => T0);
    uninstall();
    uninstall = undefined;
    expect(Date).toBe(RealDate);
    expect(Date.now()).not.toBe(T0);
  });
});
