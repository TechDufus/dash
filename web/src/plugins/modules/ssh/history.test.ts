import { describe, expect, it } from "vitest";
import { stats } from "./fixtures";
import { HISTORY_SECONDS, mergeSamples, toSample } from "./history";
import { chartRows } from "./live-chart";
import type { Sample } from "./model.gen";

const sample = (t: number, cpuRatio = 0.1): Sample => ({
  t,
  cpuRatio,
  memRatio: 0.5,
  load1: 0,
  netRxBytesPerSec: 0,
  netTxBytesPerSec: 0,
  diskReadBytesPerSec: 0,
  diskWriteBytesPerSec: 0,
});

describe("mergeSamples", () => {
  it("keeps one sample per time, the later one, oldest first", () => {
    const merged = mergeSamples(
      [sample(10), sample(20), sample(15)],
      [sample(20, 0.9), sample(25)],
    );
    expect(merged.map((s) => [s.t, s.cpuRatio])).toEqual([
      [10, 0.1],
      [15, 0.1],
      [20, 0.9],
      [25, 0.1],
    ]);
  });

  it("keeps at most an hour before the newest sample", () => {
    const old = Array.from({ length: 800 }, (_, i) => sample(1_000 + i * 5));
    const newest = 1_000 + 800 * 5;
    const merged = mergeSamples(old, [sample(newest)]);
    expect(merged.at(-1)?.t).toBe(newest);
    expect(merged[0]?.t).toBe(newest - HISTORY_SECONDS);
    expect(merged.every((s) => s.t >= newest - HISTORY_SECONDS)).toBe(true);
    expect(merged).toHaveLength(HISTORY_SECONDS / 5 + 1);
  });
});

describe("toSample", () => {
  it("cuts a topic sample down to the history's shape, at whole seconds", () => {
    expect(
      toSample(stats({ sampledAt: "2026-11-11T10:00:00.750Z", memUsedBytes: 1, memTotalBytes: 4 })),
    ).toEqual({
      t: Date.parse("2026-11-11T10:00:00Z") / 1000,
      cpuRatio: 0.25,
      memRatio: 0.25,
      load1: 0.5,
      netRxBytesPerSec: 1024,
      netTxBytesPerSec: 2048,
      diskReadBytesPerSec: 4096,
      diskWriteBytesPerSec: 8192,
      cpuTempC: 54,
    });
  });

  it("leaves out a CPU temperature the host does not report", () => {
    const { cpuTempC: _, ...without } = stats();
    expect(toSample(without)).not.toHaveProperty("cpuTempC");
  });
});

describe("chartRows", () => {
  it("breaks the line where the host was away", () => {
    const lines = [{ field: "cpuRatio" as const, label: "CPU", color: "red" }];
    const rows = chartRows([sample(0), sample(5), sample(100), sample(105)], lines, 5);
    expect(rows).toEqual([
      { t: 0, CPU: 0.1 },
      { t: 5, CPU: 0.1 },
      { t: 6, CPU: null },
      { t: 100, CPU: 0.1 },
      { t: 105, CPU: 0.1 },
    ]);
  });
});
