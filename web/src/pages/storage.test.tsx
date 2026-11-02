import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { PluginInfo, StorageData } from "@/api/types";
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
  pluginInfo({ id: "kubernetes", state, topics: ["cluster", "storage", "apps"] });

const storage = (): StorageData =>
  ({
    pvcs: [],
    buckets: [],
    rustfs: { usedBytes: 1, usableBytes: 2 },
    cnpg: [],
    nasBackups: [],
    backupJobs: [],
    etcd: { dbBytes: 1, quotaBytes: 2 },
  }) as StorageData;

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
    "NAS backups",
    "etcd database",
  ];
  const panelNames = () => screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);

  it("draws one panel per contribution, in the page's layout", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes()]);
    stream.send("storage", storage());
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
    stream.send("storage", storage());

    expect(panelNames()).toEqual(names.filter((n) => n !== "Proxmox storage"));
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
  });

  it("drops the Proxmox panel when proxmox is off", () => {
    renderApp(<StoragePage />);
    stream.plugins([proxmox(), kubernetes()]);
    stream.send("storage", storage());
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
    stream.send("storage", storage());
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
});
