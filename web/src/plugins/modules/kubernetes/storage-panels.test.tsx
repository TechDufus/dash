import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { TypedEnvelope } from "@/api/topics";
import type { BackupJob, CNPGCluster, PVC, StorageData } from "@/api/types";
import { envelope, renderApp } from "@/test/harness";
import { BackupEtcdPanel, CnpgPanel, ObjectStoragePanel, PvcPanel } from "./storage-panels";

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

const env = (over: Partial<StorageData> = {}): TypedEnvelope<"kubernetes.storage"> =>
  envelope("kubernetes.storage", {
    pvcs: [],
    buckets: [],
    rustfs: { usedBytes: 1, usableBytes: 2 },
    cnpg: [],
    backupJobs: [],
    etcd: { dbBytes: 1, quotaBytes: 2 },
    ...over,
  }) as unknown as TypedEnvelope<"kubernetes.storage">;

const job = (name: string, ageSeconds?: number): BackupJob => ({
  ref: `kubernetes:BackupJob/backup/${name}`,
  name,
  lastSuccessAgeSeconds: ageSeconds,
});

describe("the backup jobs", () => {
  const badgeOf = (name: string) => {
    const row = screen.getByText(name).parentElement;
    if (!row) throw new Error(`no row ${name}`);
    return row;
  };

  // [age in seconds, badge]: the thresholds are 36 h and 72 h, both exclusive.
  const cases: [string, number, string][] = [
    ["a fresh run", 1 * HOUR, "Fresh"],
    ["exactly 36 h", 36 * HOUR, "Fresh"],
    ["one second past 36 h", 36 * HOUR + 1, "Stale >36 h"],
    ["exactly 72 h", 72 * HOUR, "Stale >36 h"],
    ["one second past 72 h", 72 * HOUR + 1, "Stale >72 h"],
  ];
  for (const [what, age, badge] of cases) {
    it(`says ${badge} for ${what}`, () => {
      renderApp(<BackupEtcdPanel env={env({ backupJobs: [job("nightly", age)] })} />);
      expect(within(badgeOf("nightly")).getByText(badge)).toBeInTheDocument();
    });
  }

  it("says no success was recorded for a job without an age", () => {
    renderApp(<BackupEtcdPanel env={env({ backupJobs: [job("never")] })} />);
    expect(within(badgeOf("never")).getByText("No success recorded")).toBeInTheDocument();
  });

  it("lists one row per job, in the order the plugin sent them, and the etcd database", () => {
    renderApp(<BackupEtcdPanel env={env({ backupJobs: [job("b", HOUR), job("a", HOUR)] })} />);
    const names = screen.getAllByText(/^[ab]$/).map((e) => e.textContent);
    expect(names).toEqual(["b", "a"]);
    expect(screen.getByRole("heading", { name: "etcd database" })).toBeInTheDocument();
  });

  it("says there are none, and draws a skeleton before the envelope arrives", () => {
    const { rerender } = renderApp(<BackupEtcdPanel env={undefined} />);
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    rerender(<BackupEtcdPanel env={env()} />);
    expect(screen.getByText("No backup jobs.")).toBeInTheDocument();
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
    renderApp(<PvcPanel env={env({ pvcs: volumes })} />);
    expect(names()).toEqual(["media/full", "media/warm", "media/calm"]);
  });

  it("filters by namespace, name and class, and says when nothing matches", async () => {
    renderApp(<PvcPanel env={env({ pvcs: volumes })} />);
    await userEvent.type(screen.getByRole("textbox", { name: "Search volumes" }), "warm");
    expect(names()).toEqual(["media/warm"]);
    expect(screen.getByText(/1 of 3 volumes/)).toBeInTheDocument();
    await userEvent.clear(screen.getByRole("textbox", { name: "Search volumes" }));
    await userEvent.type(screen.getByRole("textbox", { name: "Search volumes" }), "zzz");
    expect(screen.getByText("Nothing matches.")).toBeInTheDocument();
  });

  it("says there are no claims", () => {
    renderApp(<PvcPanel env={env()} />);
    expect(screen.getByText("No persistent volume claims.")).toBeInTheDocument();
  });

  it("computes the days to full from the growth, and shows a dash without one", () => {
    renderApp(
      <PvcPanel
        env={env({
          pvcs: [
            pvc("growing", { growthBytesPerDay: 10 * 2 ** 30, usedBytes: 90 * 2 ** 30 }),
            pvc("flat", { growthBytesPerDay: 0 }),
          ],
        })}
      />,
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

describe("the object storage and Postgres panels", () => {
  it("draws each bucket and the RustFS drive", () => {
    renderApp(
      <ObjectStoragePanel
        env={env({
          buckets: [{ name: "media", usedBytes: 5, quotaBytes: 10, health: "ok" } as never],
        })}
      />,
    );
    expect(screen.getByText("media")).toBeInTheDocument();
    expect(screen.getByText("RustFS drive (usable)")).toBeInTheDocument();
  });

  it("draws each CNPG cluster with its ready instances", () => {
    const cluster = {
      namespace: "db",
      name: "main",
      instances: 3,
      readyInstances: 2,
      phase: "Cluster in healthy state",
      lastBackupAgeSeconds: HOUR,
      links: [],
    } as CNPGCluster;
    renderApp(<CnpgPanel env={env({ cnpg: [cluster] })} />);
    expect(screen.getByText("2/3")).toBeInTheDocument();
    expect(screen.getByText("main")).toBeInTheDocument();
  });
});
