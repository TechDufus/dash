import { screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { Route } from "wouter";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { me } from "@/settings/fixtures";
import { type FetchStub, renderApp, stubFetch } from "@/test/harness";
import { host, hostsData, info, sshRow, stats } from "./fixtures";
import { historyKey, historyPath } from "./history";
import { HostsPage } from "./hosts-page";
import type { History, Host, Sample } from "./model.gen";

// Motion reads prefers-reduced-motion once; with it on the page draws at once.
const matchMedia = window.matchMedia;
window.matchMedia = (query: string) => ({
  ...matchMedia(query),
  matches: query.includes("prefers-reduced-motion"),
});

const T0 = Date.parse("2026-11-11T10:00:00Z") / 1000;

const sample = (t: number, cpuRatio: number): Sample => ({
  t,
  cpuRatio,
  memRatio: 0.25,
  load1: 0.5,
  netRxBytesPerSec: 100,
  netTxBytesPerSec: 200,
  diskReadBytesPerSec: 300,
  diskWriteBytesPerSec: 400,
});

const ALL_STATES = [
  "up",
  "unverified",
  "key_changed",
  "unauthorized",
  "unreachable",
  "unsupported",
  "connecting",
];

describe("the Hosts page", () => {
  let stream: TestStream;
  let stub: FetchStub;
  const history: Record<string, History> = {};

  const routes = (role: "admin" | "viewer") => ({
    "GET /api/v1/me": me(role),
    ...Object.fromEntries(
      ["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf"].map((n) => [
        `GET ${historyPath(n)}`,
        () => history[n] ?? { intervalSeconds: 5, samples: [] },
      ]),
    ),
  });

  beforeEach(() => {
    for (const k of Object.keys(history)) delete history[k];
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const drawAt = (path: string, hosts: Host[] | undefined, role: "admin" | "viewer" = "admin") => {
    stub = stubFetch(routes(role));
    const r = renderApp(<Route path="/hosts/:host?">{(p) => <HostsPage host={p.host} />}</Route>, {
      path,
    });
    stream.plugins([sshRow()]);
    if (hosts) stream.send("ssh.hosts", hostsData(hosts));
    return r;
  };

  it("shows a skeleton until the topic arrives", () => {
    drawAt("/hosts", undefined);
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(screen.queryByText(/No host is set up yet/)).toBeNull();
  });

  it("says no host is set up when the plugin runs without hosts, with the way in for an administrator", async () => {
    drawAt("/hosts", []);
    expect(screen.getByText(/No host is set up yet/)).toBeInTheDocument();
    expect(await screen.findByRole("link", { name: "Add a host" })).toHaveAttribute(
      "href",
      "/settings/plugins/ssh",
    );
  });

  it("offers a viewer no way into the settings when there is no host", async () => {
    drawAt("/hosts", [], "viewer");
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    expect(screen.getByText(/No host is set up yet/)).toBeInTheDocument();
    expect(screen.queryByRole("link")).toBeNull();
  });

  describe("the fleet", () => {
    const fleet = () =>
      ALL_STATES.map((s, i) =>
        host(["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf"][i] ?? s, s),
      );
    const card = (name: RegExp) => {
      const link = screen.getByRole("link", { name });
      const panel = link.closest('[data-slot="panel"]');
      if (!panel) throw new Error("no card");
      return within(panel as HTMLElement);
    };

    it("draws a card per host, each a link to the host, the first one shown below", () => {
      drawAt("/hosts", fleet());
      const list = screen.getByRole("region", { name: "Hosts" });
      const links = within(list)
        .getAllByRole("link")
        .filter((a) => a.getAttribute("href")?.startsWith("/hosts/"));
      expect(links.map((a) => a.getAttribute("aria-label"))).toEqual([
        "Host alpha, up, shown below",
        "Host bravo, key not trusted",
        "Host charlie, host key changed",
        "Host delta, not authorized",
        "Host echo, unreachable",
        "Host foxtrot, not linux",
        "Host golf, connecting",
      ]);
      expect(links.map((a) => a.getAttribute("href"))).toEqual(
        ["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf"].map((n) => `/hosts/${n}`),
      );
      expect(within(list).getByText("1 of 7 up")).toBeInTheDocument();
    });

    it("shows the CPU, memory, load, uptime and temperature of a host that is up", () => {
      drawAt("/hosts/bravo", fleet());
      const alpha = card(/^Host alpha/);
      expect(alpha.getByRole("img", { name: "CPU 25%" })).toBeInTheDocument();
      expect(alpha.getByRole("progressbar", { name: "Memory" })).toHaveAttribute(
        "aria-valuenow",
        "25",
      );
      expect(alpha.getByText("0.50 · 0.75 · 1.00")).toBeInTheDocument();
      expect(alpha.getByText("1d 1h")).toBeInTheDocument();
      expect(alpha.getByText("54°C")).toBeInTheDocument();
      expect(alpha.getByText("Debian GNU/Linux 13 (trixie)")).toBeInTheDocument();
    });

    it("shows each other state with its fixed message, and the administrator's way to fix it", async () => {
      drawAt("/hosts", fleet());
      const want: Record<string, string | null> = {
        bravo: "Finish setup in settings",
        charlie: "Finish setup in settings",
        delta: "Finish setup in settings",
        echo: "Check its settings",
        foxtrot: "Check its settings",
        golf: null,
      };
      await screen.findAllByRole("link", { name: /Finish setup in settings/ });
      for (const [name, state] of [
        ["bravo", "unverified"],
        ["charlie", "key_changed"],
        ["delta", "unauthorized"],
        ["echo", "unreachable"],
        ["foxtrot", "unsupported"],
        ["golf", "connecting"],
      ] as const) {
        const c = card(new RegExp(`^Host ${name},`));
        expect(c.getByText(`fixed text for ${state}`)).toBeInTheDocument();
        expect(c.queryByRole("img", { name: /^CPU/ })).toBeNull();
        const link = want[name];
        if (link) {
          expect(c.getByRole("link", { name: `${link} for ${name}` })).toHaveAttribute(
            "href",
            "/settings/plugins/ssh",
          );
        } else {
          expect(c.queryByRole("link", { name: /settings/ })).toBeNull();
        }
      }
    });

    it("offers a viewer no setup link anywhere", async () => {
      drawAt("/hosts/bravo", fleet(), "viewer");
      await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
      expect(
        screen.getByText("fixed text for unverified", { selector: "span" }),
      ).toBeInTheDocument();
      expect(screen.queryByRole("link", { name: /settings/i })).toBeNull();
    });
  });

  describe("the detail", () => {
    it("shows the route's host, and says so for a name it does not know", () => {
      drawAt("/hosts/nope", [host("alpha"), host("bravo")]);
      expect(screen.getByText(/No host named/)).toHaveTextContent("No host named nope.");
      expect(screen.getByRole("link", { name: "Back to all hosts" })).toHaveAttribute(
        "href",
        "/hosts",
      );
    });

    it("draws every fact and section a host reports", async () => {
      const full = host("alpha", "up", {
        info: info({
          vendor: "Example Inc.",
          product: "Box 1",
          virtualization: "kvm",
          disks: [{ name: "nvme0n1", model: "Example NVMe", sizeBytes: 2 ** 40, kind: "nvme" }],
          filesystems: [
            { mount: "/", device: "/dev/nvme0n1p2", type: "ext4", usedBytes: 50, totalBytes: 100 },
          ],
          pools: [{ name: "tank", health: "DEGRADED", allocBytes: 1, sizeBytes: 2 }],
          containers: {
            runtime: "docker",
            denied: false,
            items: [
              {
                name: "web",
                image: "docker.io/library/nginx:1",
                state: "running",
                status: "Up 3 days",
              },
              { name: "job", image: "example/job:2", state: "exited", status: "Exited (0)" },
            ],
          },
          services: { running: 42, failed: ["backup.service"] },
        }),
        stats: stats({
          swapTotalBytes: 2 ** 30,
          swapUsedBytes: 2 ** 29,
          sensors: [{ chip: "coretemp", label: "Package id 0", tempC: 55 }],
          fans: [{ chip: "nct6775", label: "fan1", rpm: 1200 }],
        }),
      });
      drawAt("/hosts/alpha", [full]);
      const facts = Object.fromEntries(
        screen
          .getAllByRole("term")
          .map((dt) => [dt.textContent, dt.nextElementSibling?.textContent]),
      );
      expect(facts).toMatchObject({
        OS: "Debian GNU/Linux 13 (trixie)",
        Kernel: "6.12.0-1-amd64",
        Architecture: "x86_64",
        System: "Example Inc. Box 1",
        Virtualization: "kvm",
        Processor: "Example CPU · 8 CPUs",
        Memory: "16.0 GiB",
        Uptime: "1d 1h",
      });
      expect(screen.getByRole("progressbar", { name: "Swap" })).toBeInTheDocument();
      for (const title of ["CPU", "Memory", "Network", "Disk I/O"]) {
        expect(await screen.findByRole("figure", { name: title })).toBeInTheDocument();
      }
      expect(screen.getByRole("heading", { name: "Temperatures" })).toBeInTheDocument();
      expect(screen.getByText("Package id 0")).toBeInTheDocument();
      expect(screen.getByRole("img", { name: "fan1 1,200 RPM" })).toBeInTheDocument();
      expect(screen.getByRole("progressbar", { name: "/" })).toHaveAttribute("aria-valuenow", "50");
      expect(screen.getByText("Example NVMe")).toBeInTheDocument();
      expect(screen.getByText("DEGRADED")).toBeInTheDocument();
      expect(screen.getByText("1 of 2 running")).toBeInTheDocument();
      expect(screen.getByText("web")).toBeInTheDocument();
      expect(
        within(screen.getByRole("list", { name: "Failed units" })).getByText("backup.service"),
      ).toBeInTheDocument();
    });

    it("leaves out what a host does not report instead of drawing zeros", async () => {
      const bare = host("alpha", "up", {
        info: info({ kernel: "", arch: "", cpuModel: "", cpus: 0 }),
        stats: (() => {
          const { cpuTempC: _, ...s } = stats({
            memTotalBytes: 0,
            memUsedBytes: 0,
            uptimeSeconds: 0,
          });
          return s;
        })(),
      });
      drawAt("/hosts/alpha", [bare]);
      await screen.findByRole("figure", { name: "CPU" });
      const terms = screen.getAllByRole("term").map((dt) => dt.textContent);
      expect(terms).toEqual(["OS", "Load"]);
      expect(screen.queryByRole("figure", { name: "Memory" })).toBeNull();
      expect(screen.queryByRole("progressbar", { name: "Memory" })).toBeNull();
      for (const heading of [
        "Temperatures",
        "Fans",
        "File systems",
        "Disks",
        "ZFS pools",
        "Containers",
        "Systemd services",
      ]) {
        expect(screen.queryByRole("heading", { name: heading })).toBeNull();
      }
    });

    it("says what denied container access means", () => {
      drawAt("/hosts/alpha", [
        host("alpha", "up", {
          info: info({ containers: { runtime: "docker", denied: true, items: [] } }),
        }),
      ]);
      expect(
        screen.getByText(/the SSH user may not ask it which containers it runs/),
      ).toBeInTheDocument();
      expect(screen.getByText(/Access to Docker is access to the whole host/)).toBeInTheDocument();
      expect(screen.queryByText(/running$/)).toBeNull();
    });

    it("shows a host that is away with its state, message and the administrator's link", async () => {
      drawAt("/hosts/alpha", [host("alpha", "unauthorized")]);
      const status = screen.getByRole("status");
      expect(status).toHaveTextContent("fixed text for unauthorized");
      expect(
        await within(status).findByRole("link", { name: "Finish setup in settings for alpha" }),
      ).toHaveAttribute("href", "/settings/plugins/ssh");
    });
  });

  describe("the charts", () => {
    const summary = (title: string) =>
      screen.getByRole("figure", { name: title }).querySelector(".sr-only")?.textContent;

    it("read the history once and extend it with each new sample, without duplicates", async () => {
      history.alpha = { intervalSeconds: 5, samples: [sample(T0 - 10, 0.1), sample(T0 - 5, 0.2)] };
      const { queryClient } = drawAt("/hosts/alpha", [
        host("alpha", "up", {
          stats: stats({ sampledAt: new Date((T0 - 5) * 1000).toISOString(), cpuRatio: 0.2 }),
        }),
      ]);
      await waitFor(() => expect(summary("CPU")).toBe("Latest: CPU 20%."));
      const cached = () => queryClient.getQueryData<History>(historyKey("alpha"))?.samples;
      // The topic's sample is one the history already holds: nothing is added.
      expect(cached()?.map((s) => s.t)).toEqual([T0 - 10, T0 - 5]);

      const next = (t: number, cpu: number) =>
        stream.send(
          "ssh.hosts",
          hostsData([
            host("alpha", "up", {
              stats: stats({ sampledAt: new Date(t * 1000).toISOString(), cpuRatio: cpu }),
            }),
          ]),
        );
      next(T0, 0.42);
      await waitFor(() => expect(summary("CPU")).toBe("Latest: CPU 42%."));
      next(T0, 0.42);
      next(T0 + 5, 0.5);
      await waitFor(() => expect(summary("CPU")).toBe("Latest: CPU 50%."));
      expect(cached()?.map((s) => s.t)).toEqual([T0 - 10, T0 - 5, T0, T0 + 5]);
      expect(summary("Network")).toBe("Latest: Receive 1.0 KiB/s, Transmit 2.0 KiB/s.");
      expect(stub.callsTo(`GET ${historyPath("alpha")}`)).toHaveLength(1);
    });

    it("keep no more than the last hour", async () => {
      history.alpha = {
        intervalSeconds: 5,
        samples: Array.from({ length: 720 }, (_, i) => sample(T0 - 3600 + i * 5, 0.1)),
      };
      const { queryClient } = drawAt("/hosts/alpha", [
        host("alpha", "up", {
          stats: stats({ sampledAt: new Date((T0 - 5) * 1000).toISOString() }),
        }),
      ]);
      await waitFor(() =>
        expect(queryClient.getQueryData<History>(historyKey("alpha"))?.samples).toHaveLength(720),
      );
      stream.send(
        "ssh.hosts",
        hostsData([
          host("alpha", "up", {
            stats: stats({ sampledAt: new Date((T0 + 60) * 1000).toISOString() }),
          }),
        ]),
      );
      await waitFor(() =>
        expect(queryClient.getQueryData<History>(historyKey("alpha"))?.samples.at(-1)?.t).toBe(
          T0 + 60,
        ),
      );
      const samples = queryClient.getQueryData<History>(historyKey("alpha"))?.samples ?? [];
      expect(samples[0]?.t).toBeGreaterThanOrEqual(T0 + 60 - 3600);
      expect(samples).toHaveLength(709);
    });
  });
});
