import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { BackupJob, PVC, StorageData } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { BackupEtcdPanel, PvcPanel } from "./storage-panels";

const HOUR = 3600;

const pvc = (name: string, over: Partial<PVC> = {}): PVC =>
  ({
    ref: `kubernetes:PVC/media/${name}`,
    namespace: "media",
    name,
    storageClass: "fast",
    phase: "Bound",
    capacityBytes: 100 * 2 ** 30,
    usedBytes: 10 * 2 ** 30,
    usedRatio: 0.1,
    health: "ok",
    links: [],
    ...over,
  }) as PVC;

const storage = (over: Partial<StorageData> = {}): StorageData =>
  ({
    pvcs: [],
    backupJobs: [],
    etcd: { dbBytes: 1, quotaBytes: 2 },
    ...over,
  }) as unknown as StorageData;

const job = (name: string, ageSeconds?: number): BackupJob => ({
  ref: `kubernetes:BackupJob/backup/${name}`,
  name,
  lastSuccessAgeSeconds: ageSeconds,
});

// The panels read kubernetes.storage themselves: every test draws one through the stream.
let stream: TestStream;
beforeEach(() => {
  stubFetch();
  stream = connectStream();
});
afterEach(() => stream.close());

/**
 * Draws a panel on a stream whose kubernetes row lists the storage topic (the stream listens only
 * to topics a row lists), then delivers the topic: the panel is on screen before it arrives.
 */
function show(ui: ReactElement, data?: StorageData) {
  const view = renderApp(ui);
  stream.plugins([pluginInfo({ id: "kubernetes", topics: ["kubernetes.storage"] })]);
  if (data) stream.send("kubernetes.storage", data);
  return view;
}

describe("the backup jobs", () => {
  const badgeOf = (name: string) => {
    const row = screen.getByText(name).parentElement;
    if (!row) throw new Error(`no row ${name}`);
    return row;
  };

  // The 36 h and 72 h boundaries are pinned once, in components/backup-age.test.tsx.
  it("draws each job's age and freshness badge", () => {
    show(<BackupEtcdPanel />, storage({ backupJobs: [job("nightly", 80 * HOUR)] }));
    expect(within(badgeOf("nightly")).getByText("Stale >72 h")).toBeInTheDocument();
  });

  it("says no success was recorded for a job without an age", () => {
    show(<BackupEtcdPanel />, storage({ backupJobs: [job("never")] }));
    expect(within(badgeOf("never")).getByText("No success recorded")).toBeInTheDocument();
  });

  it("lists one row per job, in the order the plugin sent them, and the etcd database", () => {
    show(<BackupEtcdPanel />, storage({ backupJobs: [job("b", HOUR), job("a", HOUR)] }));
    const names = screen.getAllByText(/^[ab]$/).map((e) => e.textContent);
    expect(names).toEqual(["b", "a"]);
    expect(screen.getByRole("heading", { name: "etcd database" })).toBeInTheDocument();
  });

  it("says there are none, and draws a skeleton before the topic arrives", () => {
    show(<BackupEtcdPanel />);
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(screen.queryByText("No backup jobs.")).toBeNull();
    stream.send("kubernetes.storage", storage());
    expect(screen.getByText("No backup jobs.")).toBeInTheDocument();
    expect(document.querySelector("[aria-busy=true]")).toBeNull();
  });
});

describe("the persistent volumes", () => {
  const names = () =>
    screen
      .getAllByRole("row")
      .slice(1)
      .map((r) => r.querySelector("td")?.textContent);

  const volumes = [
    pvc("calm"),
    pvc("full", { usedRatio: 0.95, health: "crit" }),
    pvc("warm", { usedRatio: 0.8, health: "warn" }),
  ];

  it("lists the worst first", () => {
    show(<PvcPanel />, storage({ pvcs: volumes }));
    expect(names()).toEqual(["media/full", "media/warm", "media/calm"]);
  });

  it("filters by namespace, name and class, and says when nothing matches", async () => {
    show(<PvcPanel />, storage({ pvcs: volumes }));
    await userEvent.type(screen.getByRole("textbox", { name: "Search volumes" }), "warm");
    expect(names()).toEqual(["media/warm"]);
    expect(screen.getByText(/1 of 3 volumes/)).toBeInTheDocument();
    await userEvent.clear(screen.getByRole("textbox", { name: "Search volumes" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Search volumes" }), "zzz");
    expect(screen.getByText("Nothing matches.")).toBeInTheDocument();
  });

  it("says there are no claims", () => {
    show(<PvcPanel />, storage());
    expect(screen.getByText("No persistent volume claims.")).toBeInTheDocument();
  });

  it("draws a skeleton before the topic arrives", () => {
    show(<PvcPanel />);
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(screen.queryByRole("heading", { name: "Persistent volumes" })).toBeNull();
  });

  it("computes the days to full from the growth, and shows a dash without one", () => {
    show(
      <PvcPanel />,
      storage({
        pvcs: [
          pvc("growing", { growthBytesPerDay: 10 * 2 ** 30, usedBytes: 90 * 2 ** 30 }),
          pvc("flat", { growthBytesPerDay: 0 }),
        ],
      }),
    );
    const row = (name: string) => {
      const r = screen.getByText(name).closest("tr");
      if (!r) throw new Error(`no row ${name}`);
      return r;
    };
    expect(within(row("growing")).getByText(/1\.0 d/)).toBeInTheDocument();
    expect(within(row("flat")).getByText("0 B/d")).toBeInTheDocument();
  });
});
