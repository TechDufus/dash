import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PluginInfo, PVETask, VM, VMDetail, VMsData } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { VMsPage } from "./vms-page";

const proxmox = (state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "proxmox",
    state,
    topics: ["proxmox.vms", "proxmox.node", "proxmox.storage"],
    nav: [{ path: "/vms", label: "VMs", title: "Virtual machines", icon: "Server" }],
  });

const hardware = (state = "healthy"): PluginInfo =>
  pluginInfo({ id: "hardware", state, topics: ["hardware.hosts", "hardware.machines"] });

const vm = (vmid: number, name: string, over: Partial<VM> = {}): VM => ({
  ref: `proxmox:VM/${vmid}`,
  vmid,
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

const task = (upid: string, type: string, status: string): PVETask => ({
  upid,
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
    vm(102, "nas-vm", { status: "stopped", health: "warn" }),
    vm(900, "debian-template", { template: true, status: "stopped" }),
  ],
  tasks: [task("UPID:1", "qmstart", "OK"), task("UPID:2", "vzdump", "job errors")],
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

    it("offers the host shell link that hardware.hosts carries", async () => {
      const user = userEvent.setup();
      stubFetch({ "GET /api/v1/plugins/proxmox/vms/101": detail() });
      renderApp(<VMsPage vmid="101" />, { path: "/vms/101" });
      stream.plugins([proxmox(), hardware()]);
      stream.send("proxmox.vms", data());
      stream.send("hardware.hosts", { name: "pve-1", links: [hostShell] });

      const sheet = await screen.findByRole("dialog");
      await user.click(await within(sheet).findByRole("tab", { name: "Links" }));
      const link = await within(sheet).findByRole("link", { name: /Host shell/ });
      expect(link).toHaveAttribute("href", hostShell.href);
      expect(within(sheet).getByText("Host")).toBeInTheDocument();
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
