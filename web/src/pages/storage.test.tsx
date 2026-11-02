import { screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { BackupJob, PluginInfo, StorageData } from "@/api/types";
import type { PVEStorageData } from "@/plugins/modules/proxmox/model.gen";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { StoragePage } from "./storage";

const proxmox = (state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "proxmox",
    state,
    topics: ["proxmox.vms", "proxmox.node", "proxmox.storage"],
  });

const kubernetes = (state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "kubernetes",
    state,
    topics: ["kubernetes.cluster", "kubernetes.storage", "kubernetes.apps"],
  });

const storage = (backupJobs: BackupJob[] = []): StorageData =>
  ({
    pvcs: [],
    buckets: [],
    rustfs: { usedBytes: 1, usableBytes: 2 },
    cnpg: [],
    backupJobs,
    etcd: { dbBytes: 1, quotaBytes: 2 },
  }) as unknown as StorageData;

const pveStorage = (): PVEStorageData => ({
  storages: [{ ref: "proxmox:Storage/local-zfs", name: "local-zfs", usedBytes: 1, sizeBytes: 2 }],
});

describe("Storage", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const names = [
    "Persistent volumes",
    "Object storage",
    "Proxmox storage",
    "Postgres clusters (CNPG)",
    "Backup jobs",
    "etcd database",
  ];
  const panelNames = () => screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);

  it("draws one panel per contribution, in the page's layout", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes()]);
    stream.send("kubernetes.storage", storage());
    stream.send("proxmox.storage", pveStorage());

    expect(panelNames()).toEqual(names);
    const cell = (name: string) =>
      screen.getByRole("heading", { name }).closest("div.lg\\:col-span-2");
    expect(cell("Persistent volumes")).not.toBeNull();
    for (const half of names.slice(1)) expect(cell(half), half).toBeNull();
  });

  it("draws the Proxmox panel's own skeleton in its cell until proxmox.storage arrives", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes()]);
    stream.send("kubernetes.storage", storage());

    expect(panelNames()).toEqual(names.filter((n) => n !== "Proxmox storage"));
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
  });

  it("drops the Proxmox panel when proxmox is off", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes()]);
    stream.send("kubernetes.storage", storage());
    stream.send("proxmox.storage", pveStorage());

    stream.plugins([proxmox("disabled"), kubernetes()]);
    expect(panelNames()).toEqual(names.filter((n) => n !== "Proxmox storage"));
  });

  it("draws only the Proxmox panel when kubernetes is off, with no explanation and no skeleton", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes("disabled")]);
    stream.send("proxmox.storage", pveStorage());

    expect(panelNames()).toEqual(["Proxmox storage"]);
    expect(screen.getByText("local-zfs")).toBeInTheDocument();
    expect(screen.queryByText(/Storage data comes from/)).toBeNull();
    expect(document.querySelector("[aria-busy=true]")).toBeNull();
  });

  it("shows the Proxmox panel's own skeleton when only proxmox is on and its topic is pending", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes("disabled")]);

    expect(screen.queryAllByRole("heading", { level: 2 })).toEqual([]);
    expect(screen.queryByText(/Storage data comes from/)).toBeNull();
    expect(document.querySelectorAll("[aria-busy=true]")).toHaveLength(1);
  });

  it("shows one explanation when kubernetes and proxmox are both off", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes()]);
    stream.send("kubernetes.storage", storage());
    stream.send("proxmox.storage", pveStorage());

    stream.plugins([proxmox("unavailable"), kubernetes("disabled")]);
    expect(screen.queryAllByRole("heading", { level: 2 })).toEqual([]);
    expect(
      screen.getByText(
        "Storage data comes from the kubernetes plugin, which is disabled, and the proxmox plugin, which is unavailable.",
      ),
    ).toBeInTheDocument();
    expect(screen.getAllByText(/Storage data comes from/)).toHaveLength(1);
    expect(document.querySelector("[aria-busy=true]")).toBeNull();
  });

  it("shows the skeleton until the storage topic arrives", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes()]);
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(screen.queryAllByRole("heading", { level: 2 })).toEqual([]);
  });

  it("lists each backup job with the age of its last run, and says so when there is none", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes()]);
    stream.send(
      "kubernetes.storage",
      storage([
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
    const rows = within(panel as HTMLElement);
    expect(rows.getByText("nightly")).toBeInTheDocument();
    expect(rows.getByText("Fresh")).toBeInTheDocument();
    expect(rows.getByText("Stale >72 h")).toBeInTheDocument();
    expect(rows.getByText("never")).toBeInTheDocument();
    expect(rows.queryByText(/No backup jobs/)).toBeNull();

    stream.send("kubernetes.storage", storage());
    expect(screen.getByText("No backup jobs.")).toBeInTheDocument();
  });
});
