// The demo's clock: the page reads the time of the recording, not the visitor's, so relative times
// ("3 minutes ago"), chart ranges and staleness read as they did when it was recorded. Timers and
// performance.now are left alone; only Date.now() and a Date built without arguments move.

/**
 * The recording's time: the recorded instant of the frame being shown plus the real time since it
 * was shown. `elapsed` is a parameter so a test can drive it.
 */
export class RecordedClock {
  private at: number;
  private shownAt: number;

  constructor(
    at: number,
    private readonly elapsed: () => number = () => performance.now(),
  ) {
    this.at = at;
    this.shownAt = elapsed();
  }

  /** A frame recorded at `at` (milliseconds since the epoch) is on screen from now on. */
  show(at: number): void {
    this.at = at;
    this.shownAt = this.elapsed();
  }

  now(): number {
    return this.at + (this.elapsed() - this.shownAt);
  }
}

/**
 * Replaces the global Date with one whose `Date.now()` and `new Date()` read `now`; every other
 * use (a Date of a given time, `Date.parse`, `Date.UTC`) behaves as before. Returns the function
 * that puts the browser's Date back (tests). Only the demo entry calls it, before any app module
 * evaluates, so every module that reads Date sees the shim.
 */
export function installClock(now: () => number): () => void {
  const RealDate = globalThis.Date;
  class RecordedDate extends RealDate {
    // The arguments are whatever the caller passed; the cast only satisfies the overloads.
    constructor(...args: unknown[]) {
      if (args.length === 0) super(now());
      else super(...(args as [string]));
    }

    static override now(): number {
      return now();
    }
  }
  // Date() called without new returns the current time as a string, as the browser's does.
  const DateShim = new Proxy(RecordedDate, {
    apply: () => new RecordedDate().toString(),
  });
  globalThis.Date = DateShim as DateConstructor;
  return () => {
    globalThis.Date = RealDate;
  };
}
