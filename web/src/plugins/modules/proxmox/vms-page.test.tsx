import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PluginInfo, PVETask, VM, VMDetail, VMsData } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import type { NodeData, PVEClusterData, PVEStorageData } from "./model.gen";
import { VMsPage } from "./vms-page";

const proxmox = (state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "proxmox",
    state,
    topics: ["proxmox.vms", "proxmox.cluster", "proxmox.storage"],
    nav: [{ path: "/vms", label: "VMs", title: "Virtual machines", icon: "Server" }],
  });

const hardware = (state = "healthy"): PluginInfo =>
  pluginInfo({ id: "hardware", state, topics: ["hardware.hosts", "hardware.machines"] });

const vm = (vmid: number, name: string, over: Partial<VM> = {}): VM => ({
  ref: `proxmox:VM/${vmid}`,
  vmid,
  node: "pve-1",
  name,
  status: "running",
  health: "ok",
  critical: false,
  tags: [],
  cpus: 2,
  cpuRatio: 0.25,
  memBytes: 1 << 30,
  maxMemBytes: 2 << 30,
  uptimeSeconds: 3600,
  netInBytes: 1024,
  netOutBytes: 2048,
  template: false,
  lock: "",
  links: [],
  ...over,
});

const task = (upid: string, type: string, status: string, node = "pve-1"): PVETask => ({
  upid,
  node,
  type,
  id: "101",
  user: "root@pam",
  status,
  startTime: "2026-01-01T00:00:00Z",
  endTime: "2026-01-01T00:01:00Z",
});

const data = (): VMsData => ({
  vms: [
    vm(101, "plex-vm"),
    vm(102, "nas-vm", { node: "pve-2", status: "stopped", health: "warn" }),
    vm(900, "debian-template", { node: "pve-2", template: true, status: "stopped" }),
  ],
  tasks: [
    task("UPID:pve-1:1", "qmstart", "OK"),
    task("UPID:pve-2:2", "vzdump", "job errors", "pve-2"),
  ],
});

const GiB = 1024 ** 3;

const node = (name: string, over: Partial<NodeData> = {}): NodeData => ({
  ref: `proxmox:Node/${name}`,
  node: name,
  status: "online",
  cpuRatio: 0.5,
  threads: 16,
  memUsedBytes: 32 * GiB,
  memTotalBytes: 64 * GiB,
  rootUsedBytes: 10 * GiB,
  rootTotalBytes: 100 * GiB,
  uptimeSeconds: 2 * 86400 + 3 * 3600,
  version: "9.2.2",
  kernel: "6.14.8-2-pve",
  cpuModel: "Example CPU 2.4GHz",
  cpuSockets: 1,
  cpuCores: 8,
  ...over,
});

const cluster = (over: Partial<PVEClusterData> = {}): PVEClusterData => ({
  ref: "proxmox:Cluster/lab",
  name: "lab",
  standalone: false,
  quorumKnown: true,
  quorate: true,
  nodes: [node("pve-1"), node("pve-2", { threads: 8 })],
  totals: {
    nodes: 2,
    nodesOnline: 2,
    threads: 24,
    cpuRatio: 0.5,
    memUsedBytes: 64 * GiB,
    memTotalBytes: 128 * GiB,
  },
  ...over,
});

const storage = (): PVEStorageData => ({
  storages: [
    {
      ref: "proxmox:Storage/nfs",
      name: "nfs",
      node: "",
      shared: true,
      usedBytes: 100 * GiB,
      sizeBytes: 1000 * GiB,
    },
  ],
  usedBytes: 100 * GiB,
  sizeBytes: 1000 * GiB,
});

const detail = (): VMDetail => ({
  vm: vm(101, "plex-vm"),
  config: {
    cores: 2,
    sockets: 1,
    memoryMiB: 2048,
    balloonMiB: 0,
    ostype: "l26",
    agent: "1",
    disks: [],
    nics: [],
  },
  ips: [],
  snapshots: [],
});

/** The Guests line of a node card. */
const guestsFact = (card: HTMLElement) => within(card).getByText("Guests").parentElement;

describe("the VMs page", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => stream.close());

  it("draws the cards, the templates and the tasks from proxmox.vms", async () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());

    expect(await screen.findByRole("link", { name: "plex-vm, VM 101, running" })).toHaveAttribute(
      "href",
      "/vms/101",
    );
    expect(screen.getByRole("link", { name: "nas-vm, VM 102, stopped" })).toBeInTheDocument();
    // A template is listed under Templates, never as a card.
    expect(screen.queryByRole("link", { name: /debian-template, VM 900/ })).toBeNull();
    const templates = screen.getByText("Templates").closest("details") as HTMLElement;
    expect(within(templates).getByText("debian-template")).toBeInTheDocument();
    // The tasks.
    expect(screen.getByText("qmstart")).toBeInTheDocument();
    expect(screen.getByText("vzdump")).toBeInTheDocument();
    expect(screen.getByText("Failed: job errors")).toBeInTheDocument();
  });

  it("puts each guest's node on its card, its template row and its tasks", async () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());

    const plex = await screen.findByRole("link", { name: "plex-vm, VM 101, running" });
    expect(within(plex).getByTitle("Runs on node pve-1")).toHaveTextContent("pve-1");
    const nas = screen.getByRole("link", { name: "nas-vm, VM 102, stopped" });
    expect(within(nas).getByTitle("Runs on node pve-2")).toBeInTheDocument();
    const templates = screen.getByText("Templates").closest("details") as HTMLElement;
    expect(within(templates).getByTitle("Runs on node pve-2")).toBeInTheDocument();

    const table = screen.getByRole("table");
    const headers = within(table)
      .getAllByRole("columnheader")
      .map((h) => h.textContent);
    expect(headers).toEqual(["Type", "ID", "Node", "User", "Status", "Started", "Duration"]);
    const vzdump = screen.getByText("vzdump").closest("tr") as HTMLElement;
    expect(within(vzdump).getAllByRole("cell")[2]).toHaveTextContent("pve-2");
  });

  it("summarises the cluster and draws one card per node with its guests", async () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());
    stream.send("proxmox.cluster", cluster());
    stream.send("proxmox.storage", storage());

    expect(await screen.findByRole("heading", { name: "lab" })).toBeInTheDocument();
    expect(screen.getByText("Quorate")).toBeInTheDocument();
    expect(screen.getByText("2 of 2 nodes online")).toBeInTheDocument();
    expect(screen.getByText("24 threads")).toBeInTheDocument();
    expect(screen.getByText("64.0 GiB / 128.0 GiB")).toBeInTheDocument();
    expect(screen.getByText("100.0 GiB / 1000.0 GiB")).toBeInTheDocument();

    const one = screen.getByRole("group", { name: "Node pve-1, online" });
    expect(within(one).getByText("16 threads")).toBeInTheDocument();
    expect(within(one).getByText("2d 3h")).toBeInTheDocument();
    expect(within(one).getByText("9.2.2")).toBeInTheDocument();
    expect(within(one).getByText("Example CPU 2.4GHz")).toBeInTheDocument();
    // The template on pve-2 is not a guest the node card counts.
    expect(within(one).getByText("1 running of 1")).toBeInTheDocument();
    const two = screen.getByRole("group", { name: "Node pve-2, online" });
    expect(within(two).getByText("0 running of 1")).toBeInTheDocument();
  });

  it("marks a cluster that lost quorum and a node that is offline as critical", async () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    const vms = data();
    vms.vms.push(vm(103, "dns-vm", { node: "pve-2" }));
    stream.send("proxmox.vms", vms);
    stream.send(
      "proxmox.cluster",
      cluster({
        quorate: false,
        nodes: [node("pve-1"), node("pve-2", { status: "offline" })],
        totals: { ...cluster().totals, nodesOnline: 1 },
      }),
    );

    const quorum = await screen.findByText("Quorum lost");
    expect(quorum.closest("span.rounded-full")).toHaveClass("text-crit");
    expect(screen.getByText(/nothing can be started, stopped or changed/)).toBeInTheDocument();
    expect(screen.getByText("1 of 2 nodes online").closest("span.rounded-full")).toHaveClass(
      "text-crit",
    );

    const offline = screen.getByRole("group", { name: "Node pve-2, offline" });
    expect(within(offline).getByText("offline").closest("span.rounded-full")).toHaveClass(
      "text-crit",
    );
    // An offline node has no usage, but keeps the facts it last reported.
    expect(within(offline).getByRole("img", { name: "– CPU" })).toBeInTheDocument();
    expect(within(offline).getByText("9.2.2")).toBeInTheDocument();
    // Its guests still report running for minutes after it drops, so none is counted as running.
    expect(guestsFact(offline)).toHaveTextContent("2 guests");
  });

  it("shows a node whose status daemon went quiet as unknown, neither healthy nor down", async () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());
    stream.send(
      "proxmox.cluster",
      cluster({
        nodes: [node("pve-1"), node("pve-2", { status: "unknown" })],
        totals: { ...cluster().totals, nodesOnline: 1 },
      }),
    );

    const quiet = await screen.findByRole("group", { name: "Node pve-2, unknown" });
    const badge = within(quiet).getByText("unknown").closest("span.rounded-full");
    expect(badge).not.toHaveClass("text-ok");
    expect(badge).not.toHaveClass("text-crit");
    expect(quiet).not.toHaveClass("border-crit/30");
    expect(within(quiet).getByRole("img", { name: "– CPU" })).toBeInTheDocument();
    expect(within(quiet).getByText("Uptime").parentElement).toHaveTextContent("–");
    expect(guestsFact(quiet)).toHaveTextContent("1 guest");
    // A quiet node is not known to be down, so the count is not critical either.
    expect(screen.getByText("1 of 2 nodes online").closest("span.rounded-full")).not.toHaveClass(
      "text-crit",
    );
  });

  it("makes no quorum claim while the quorum cannot be read", async () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());
    stream.send("proxmox.cluster", cluster({ quorumKnown: false, quorate: false }));

    const quorum = await screen.findByText("Quorum unknown");
    expect(quorum.closest("span.rounded-full")).not.toHaveClass("text-ok");
    expect(quorum.closest("span.rounded-full")).not.toHaveClass("text-crit");
    expect(screen.queryByText("Quorate")).toBeNull();
    expect(screen.queryByText("Quorum lost")).toBeNull();
    expect(screen.queryByText(/nothing can be started, stopped or changed/)).toBeNull();
  });

  it("draws no cluster that has never been read", async () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    // What the plugin publishes when its first cluster poll fails.
    stream.send("proxmox.cluster", {
      ref: "",
      name: "",
      standalone: false,
      quorumKnown: false,
      quorate: false,
      nodes: [],
      totals: {
        nodes: 0,
        nodesOnline: 0,
        threads: 0,
        cpuRatio: 0,
        memUsedBytes: 0,
        memTotalBytes: 0,
      },
    } satisfies PVEClusterData);
    stream.send("proxmox.vms", data());

    expect(
      await screen.findByRole("link", { name: "plex-vm, VM 101, running" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Proxmox cluster")).toBeNull();
    expect(screen.queryByText(/Quorum/)).toBeNull();
    expect(screen.queryByText(/nodes online/)).toBeNull();
  });

  it("counts no node's guests until proxmox.vms arrives", async () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.cluster", cluster());

    const one = await screen.findByRole("group", { name: "Node pve-1, online" });
    expect(guestsFact(one)).toHaveTextContent("–");
    expect(guestsFact(one)).not.toHaveTextContent("none");
  });

  it("names a standalone server by its node and shows no quorum", async () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());
    stream.send(
      "proxmox.cluster",
      cluster({
        ref: "proxmox:Cluster/pve-1",
        name: "",
        standalone: true,
        nodes: [node("pve-1")],
        totals: { ...cluster().totals, nodes: 1, nodesOnline: 1, threads: 16 },
      }),
    );

    expect(
      await screen.findByRole("heading", { name: "Standalone server pve-1" }),
    ).toBeInTheDocument();
    expect(screen.queryByText("Quorate")).toBeNull();
    expect(screen.getByText("1 of 1 node online")).toBeInTheDocument();
  });

  it("shows the skeleton until proxmox.vms arrives", () => {
    stubFetch();
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    expect(screen.getByText("Loading VMs")).toBeInTheDocument();
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
  });

  it("opens the sheet at /vms/101 and fetches the detail from the proxmox plugin's route", async () => {
    const stub = stubFetch({ "GET /api/v1/plugins/proxmox/vms/101": detail() });
    renderApp(<VMsPage vmid="101" />, { path: "/vms/101" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());

    const sheet = await screen.findByRole("dialog");
    expect(within(sheet).getByText("plex-vm")).toBeInTheDocument();
    expect(await within(sheet).findByText("Configuration")).toBeInTheDocument();
    expect(stub.callsTo("GET /api/v1/plugins/proxmox/vms/101")).toHaveLength(1);
    expect(stub.calls.map((c) => c.path)).not.toContain("/api/vms/101");
  });

  it("copies the VM's context from the sheet", async () => {
    const writeText = vi.fn(async (_: string) => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    try {
      const stub = stubFetch({
        "GET /api/v1/plugins/proxmox/vms/101": detail(),
        "GET /api/v1/context": () => new Response("# dash context bundle\n"),
      });
      renderApp(<VMsPage vmid="101" />, { path: "/vms/101" });
      stream.plugins([{ ...proxmox(), objects: true }, hardware()]);
      stream.send("proxmox.vms", data());

      const sheet = await screen.findByRole("dialog");
      fireEvent.click(await within(sheet).findByRole("button", { name: "Copy context for AI" }));
      await waitFor(() => expect(writeText).toHaveBeenCalledWith("# dash context bundle\n"));
      expect(stub.callsTo("GET /api/v1/context").map((c) => c.query.get("ref"))).toEqual([
        "proxmox:VM/101",
      ]);
    } finally {
      Reflect.deleteProperty(navigator, "clipboard");
    }
  });

  describe("the sheet's Links tab", () => {
    const hostShell = {
      label: "Host shell",
      href: "https://pve.example.com:8006/?console=shell&novnc=1&node=pve-1&resize=off&cmd=",
      kind: "proxmox",
    };

    it("offers the host shell link when the hardware host is the VM's node", async () => {
      const user = userEvent.setup();
      stubFetch({ "GET /api/v1/plugins/proxmox/vms/101": detail() });
      renderApp(<VMsPage vmid="101" />, { path: "/vms/101" });
      stream.plugins([proxmox(), hardware()]);
      stream.send("proxmox.vms", data());
      stream.send("hardware.hosts", { name: "pve-1", node: "pve-1", links: [hostShell] });

      const sheet = await screen.findByRole("dialog");
      await user.click(await within(sheet).findByRole("tab", { name: "Links" }));
      const link = await within(sheet).findByRole("link", { name: /Host shell/ });
      expect(link).toHaveAttribute("href", hostShell.href);
      expect(within(sheet).getByText("Host")).toBeInTheDocument();
    });

    it("has no host link for a VM on another node than the hardware host", async () => {
      const user = userEvent.setup();
      stubFetch({
        "GET /api/v1/plugins/proxmox/vms/102": { ...detail(), vm: data().vms[1] },
      });
      renderApp(<VMsPage vmid="102" />, { path: "/vms/102" });
      stream.plugins([proxmox(), hardware()]);
      stream.send("proxmox.vms", data());
      stream.send("hardware.hosts", { name: "pve-1", node: "pve-1", links: [hostShell] });

      const sheet = await screen.findByRole("dialog");
      expect(within(sheet).getByTitle("Runs on node pve-2")).toBeInTheDocument();
      await user.click(await within(sheet).findByRole("tab", { name: "Links" }));
      expect(await within(sheet).findByText("No links for this VM.")).toBeInTheDocument();
      expect(within(sheet).queryByRole("link", { name: /Host shell/ })).toBeNull();
    });

    it("has no host link without the hardware plugin", async () => {
      const user = userEvent.setup();
      stubFetch({ "GET /api/v1/plugins/proxmox/vms/101": detail() });
      renderApp(<VMsPage vmid="101" />, { path: "/vms/101" });
      stream.plugins([proxmox()]);
      stream.send("proxmox.vms", data());

      const sheet = await screen.findByRole("dialog");
      await user.click(await within(sheet).findByRole("tab", { name: "Links" }));
      expect(await within(sheet).findByText("No links for this VM.")).toBeInTheDocument();
      expect(within(sheet).queryByRole("link", { name: /Host shell/ })).toBeNull();
    });
  });
});
