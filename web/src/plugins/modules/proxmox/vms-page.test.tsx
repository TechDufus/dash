import { screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { PluginInfo, PVETask, VM, VMDetail, VMsData } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import type { MachinesData } from "../hardware/model.gen";
import { VMsPage } from "./vms-page";

const proxmox = (state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "proxmox",
    state,
    topics: ["proxmox.vms", "proxmox.node", "proxmox.storage"],
    nav: [{ path: "/vms", label: "VMs", title: "Virtual machines", icon: "Server" }],
  });

const hardware = (state = "healthy"): PluginInfo =>
  pluginInfo({ id: "hardware", state, topics: ["host", "hardware.machines"] });

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

const machines = (list: MachinesData["machines"]): MachinesData => ({ machines: list });

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
});

describe("the Machines panel", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch();
    stream = connectStream();
  });
  afterEach(() => stream.close());

  it("draws hardware.machines: name, source and Up or Down", async () => {
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());
    stream.send(
      "hardware.machines",
      machines([
        { ref: "hardware:Machine/nas", name: "nas", up: true, source: "prometheus" },
        { ref: "hardware:Machine/pi", name: "pi", up: false, source: "gatus" },
      ]),
    );

    expect(await screen.findByRole("heading", { name: "Other machines" })).toBeInTheDocument();
    const nas = (await screen.findByText("nas")).closest("li") as HTMLElement;
    expect(within(nas).getByText("prometheus")).toBeInTheDocument();
    expect(within(nas).getByText("Up")).toBeInTheDocument();
    const pi = screen.getByText("pi").closest("li") as HTMLElement;
    expect(within(pi).getByText("gatus")).toBeInTheDocument();
    expect(within(pi).getByText("Down")).toBeInTheDocument();
  });

  it("says so when no machine is tracked", async () => {
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());
    stream.send("hardware.machines", machines([]));
    expect(await screen.findByText("No other machines are tracked.")).toBeInTheDocument();
  });

  it("shows a skeleton while the topic is available but has not arrived", () => {
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware()]);
    stream.send("proxmox.vms", data());
    expect(screen.getByRole("heading", { name: "Other machines" })).toBeInTheDocument();
    expect(screen.queryByText("No other machines are tracked.")).toBeNull();
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
  });

  it("is absent when the hardware plugin is off", async () => {
    renderApp(<VMsPage />, { path: "/vms" });
    stream.plugins([proxmox(), hardware("disabled")]);
    stream.send("proxmox.vms", data());
    await screen.findByRole("link", { name: "plex-vm, VM 101, running" });
    await waitFor(() => expect(screen.queryByText("Other machines")).toBeNull());
    expect(screen.queryByText("No other machines are tracked.")).toBeNull();
    // The activity panel is still there.
    expect(screen.getByRole("heading", { name: "Proxmox activity" })).toBeInTheDocument();
  });
});
