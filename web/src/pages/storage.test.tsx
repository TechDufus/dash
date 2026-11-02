import { screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type {
  BackupJob,
  CNPGClustersData,
  PluginInfo,
  RustFSUsageData,
  StorageData,
} from "@/api/types";
import type { PVEStorageData } from "@/plugins/modules/proxmox/model.gen";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { StoragePage } from "./storage";

type Id = "kubernetes" | "proxmox" | "cnpg" | "rustfs";
type States = Partial<Record<Id, string>>;

const TOPICS: Record<Id, string[]> = {
  kubernetes: ["kubernetes.cluster", "kubernetes.storage", "kubernetes.apps"],
  proxmox: ["proxmox.vms", "proxmox.node", "proxmox.storage"],
  cnpg: ["cnpg.clusters"],
  rustfs: ["rustfs.usage"],
};

/** The four storage plugins' rows, each healthy unless `states` says otherwise. */
const rows = (states: States = {}): PluginInfo[] =>
  (Object.keys(TOPICS) as Id[]).map((id) =>
    pluginInfo({ id, state: states[id] ?? "healthy", topics: TOPICS[id] }),
  );

/** Every plugin off except `ids`. */
const only = (...ids: Id[]): States =>
  Object.fromEntries(
    (Object.keys(TOPICS) as Id[]).filter((id) => !ids.includes(id)).map((id) => [id, "disabled"]),
  );

const kubernetesData = (backupJobs: BackupJob[] = []): StorageData =>
  ({
    pvcs: [],
    backupJobs,
    etcd: { dbBytes: 1, quotaBytes: 2 },
  }) as unknown as StorageData;

/** Each plugin's topic, the panel headings its data draws and a line that shows once it has. */
const DATA: Record<
  Id,
  { topic: string; payload: () => unknown; headings: string[]; shown: string }
> = {
  kubernetes: {
    topic: "kubernetes.storage",
    payload: () => kubernetesData(),
    headings: ["Persistent volumes", "Backup jobs", "etcd database"],
    shown: "No backup jobs.",
  },
  proxmox: {
    topic: "proxmox.storage",
    payload: (): PVEStorageData => ({
      storages: [
        { ref: "proxmox:Storage/local-zfs", name: "local-zfs", usedBytes: 1, sizeBytes: 2 },
      ],
    }),
    headings: ["Proxmox storage"],
    shown: "local-zfs",
  },
  cnpg: {
    topic: "cnpg.clusters",
    payload: (): CNPGClustersData => ({ clusters: [] }),
    headings: ["Postgres clusters (CNPG)"],
    shown: "No CNPG clusters.",
  },
  rustfs: {
    topic: "rustfs.usage",
    payload: (): RustFSUsageData => ({ buckets: [], capacity: { usedBytes: 1, usableBytes: 2 } }),
    headings: ["Object storage"],
    shown: "No buckets.",
  },
};

// The placement of §6 P3.12: pvcs 10, rustfs object storage 20, proxmox 30, cnpg 40, backups and
// etcd 50, and the volumes are the one full-width cell.
const ORDER = [
  "Persistent volumes",
  "Object storage",
  "Proxmox storage",
  "Postgres clusters (CNPG)",
  "Backup jobs",
  "etcd database",
];

const NO_STORAGE = "No storage data: none of the plugins that report storage is running.";

describe("Storage", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const panelNames = () => screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
  const headings = () => screen.queryAllByRole("heading", { level: 2 }).map((h) => h.textContent);
  const busy = () => document.querySelectorAll("[aria-busy=true]").length;

  /** The plugin list first (the stream listens only to topics a row lists), then the topics. */
  function open(states: States = {}, skip: Id[] = []) {
    renderApp(<StoragePage />);
    stream.plugins(rows(states));
    for (const id of Object.keys(DATA) as Id[]) {
      if (!skip.includes(id)) stream.send(DATA[id].topic, DATA[id].payload());
    }
  }

  it("draws one panel per contribution in the placement order, in the page's layout", () => {
    open();

    expect(panelNames()).toEqual(ORDER);
    const cell = (name: string) =>
      screen.getByRole("heading", { name }).closest("div.lg\\:col-span-2");
    expect(cell("Persistent volumes")).not.toBeNull();
    for (const half of ORDER.slice(1)) expect(cell(half), half).toBeNull();
    expect(screen.queryByText(NO_STORAGE)).toBeNull();
    expect(busy()).toBe(0);
  });

  describe("a visible plugin whose topic has not arrived", () => {
    // [plugin, the skeletons it draws]: the kubernetes plugin has two panels on its one topic.
    const cases: [Id, number][] = [
      ["kubernetes", 2],
      ["proxmox", 1],
      ["cnpg", 1],
      ["rustfs", 1],
    ];
    for (const [id, skeletons] of cases) {
      it(`draws ${id}'s skeleton in its cell, not "none", and the rest of the panels`, () => {
        open({}, [id]);

        expect(busy()).toBe(skeletons);
        expect(panelNames()).toEqual(ORDER.filter((n) => !DATA[id].headings.includes(n)));
        expect(screen.queryByText(NO_STORAGE)).toBeNull();
        expect(screen.queryByText(DATA[id].shown)).toBeNull();

        stream.send(DATA[id].topic, DATA[id].payload());
        expect(screen.getByText(DATA[id].shown)).toBeInTheDocument();
        expect(panelNames()).toEqual(ORDER);
        expect(busy()).toBe(0);
      });
    }

    it("draws every panel's skeleton and no sentence before any topic arrives", () => {
      open({}, ["kubernetes", "proxmox", "cnpg", "rustfs"]);

      expect(headings()).toEqual([]);
      expect(busy()).toBe(5);
      expect(screen.queryByText(NO_STORAGE)).toBeNull();
    });
  });

  describe("each plugin's state", () => {
    const ids = Object.keys(DATA) as Id[];
    for (const id of ids) {
      for (const state of ["healthy", "degraded", "starting", "error"]) {
        it(`keeps ${id}'s panel while it is ${state}`, () => {
          open({ [id]: state });
          expect(panelNames()).toEqual(ORDER);
        });
      }
      for (const state of ["unavailable", "disabled", "unconfigured", "blocked"]) {
        it(`drops ${id}'s panel while it is ${state}, which is not an error`, () => {
          open({ [id]: state });
          expect(panelNames()).toEqual(ORDER.filter((n) => !DATA[id].headings.includes(n)));
          expect(screen.queryByText(NO_STORAGE)).toBeNull();
          expect(busy()).toBe(0);
        });
      }
    }

    it("follows a plugin turning off and on again", () => {
      open();
      stream.plugins(rows({ cnpg: "unavailable" }));
      expect(panelNames()).toEqual(ORDER.filter((n) => n !== "Postgres clusters (CNPG)"));

      stream.plugins(rows());
      // A topic is dropped while its plugin is hidden; the server re-sends it when it returns.
      stream.send(DATA.cnpg.topic, DATA.cnpg.payload());
      expect(panelNames()).toEqual(ORDER);
    });
  });

  describe("the sentence", () => {
    const off = (): States => ({
      kubernetes: "disabled",
      proxmox: "unavailable",
      cnpg: "unavailable",
      rustfs: "disabled",
    });

    it("says nothing is running, once, and names no plugin, when none of the four is visible", () => {
      open(off());

      expect(screen.getAllByText(/^No storage data/)).toHaveLength(1);
      expect(screen.getByText(NO_STORAGE)).toBeInTheDocument();
      expect(NO_STORAGE).not.toMatch(/kubernetes|proxmox|cnpg|rustfs|postgres/i);
      expect(headings()).toEqual([]);
      expect(busy()).toBe(0);
    });

    it("says nothing is running when the last visible plugin goes away, and clears when one returns", () => {
      open({ rustfs: "healthy", kubernetes: "disabled", proxmox: "disabled", cnpg: "disabled" });
      expect(panelNames()).toEqual(["Object storage"]);

      stream.plugins(rows(off()));
      expect(screen.getByText(NO_STORAGE)).toBeInTheDocument();
      expect(headings()).toEqual([]);

      stream.plugins(rows({ ...off(), proxmox: "healthy" }));
      // A topic is dropped while its plugin is hidden; the server re-sends it when it returns.
      stream.send(DATA.proxmox.topic, DATA.proxmox.payload());
      expect(screen.queryByText(NO_STORAGE)).toBeNull();
      expect(panelNames()).toEqual(["Proxmox storage"]);
    });

    // Any panel visible means no sentence: each plugin alone is enough, with its own skeleton
    // while its topic is pending and its panels after.
    const alone: Id[] = ["kubernetes", "proxmox", "cnpg", "rustfs"];
    for (const id of alone) {
      it(`is absent when only ${id} is visible`, () => {
        open(only(id), [id]);
        expect(screen.queryByText(NO_STORAGE)).toBeNull();
        expect(headings()).toEqual([]);
        expect(busy()).toBe(id === "kubernetes" ? 2 : 1);

        stream.send(DATA[id].topic, DATA[id].payload());
        expect(panelNames()).toEqual(ORDER.filter((n) => DATA[id].headings.includes(n)));
        expect(screen.queryByText(NO_STORAGE)).toBeNull();
        expect(busy()).toBe(0);
      });
    }

    it("is absent when kubernetes is off and Proxmox is on", () => {
      open({ kubernetes: "disabled", cnpg: "disabled", rustfs: "disabled" });
      expect(panelNames()).toEqual(["Proxmox storage"]);
      expect(screen.getByText("local-zfs")).toBeInTheDocument();
      expect(screen.queryByText(NO_STORAGE)).toBeNull();
      expect(busy()).toBe(0);
    });
  });

  it("lists each backup job with the age of its last run, and says so when there is none", () => {
    renderApp(<StoragePage />);
    stream.plugins(rows());
    stream.send(
      "kubernetes.storage",
      kubernetesData([
        {
          ref: "kubernetes:BackupJob/backup/nightly",
          name: "nightly",
          lastSuccessAgeSeconds: 3600,
        },
        {
          ref: "kubernetes:BackupJob/backup/weekly",
          name: "weekly",
          lastSuccessAgeSeconds: 80 * 3600,
        },
        { ref: "kubernetes:BackupJob/backup/never", name: "never" },
      ]),
    );
    const panel = screen.getByRole("heading", { name: "Backup jobs" }).closest("[data-slot=panel]");
    expect(panel).not.toBeNull();
    const list = within(panel as HTMLElement);
    expect(list.getByText("nightly")).toBeInTheDocument();
    expect(list.getByText("Fresh")).toBeInTheDocument();
    expect(list.getByText("Stale >72 h")).toBeInTheDocument();
    expect(list.getByText("never")).toBeInTheDocument();
    expect(list.queryByText(/No backup jobs/)).toBeNull();

    stream.send("kubernetes.storage", kubernetesData());
    expect(screen.getByText("No backup jobs.")).toBeInTheDocument();
  });
});
