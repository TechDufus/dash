import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AppsData, LauncherData, OverviewData } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
import { AppsPage } from "./apps";
import { OverviewPage } from "./overview";

// The topology diagram measures and animates; the overview tests are about the tiles.
vi.mock("@/pages/topology", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  TopologyPanel: () => <div data-testid="topology" />,
}));

// Motion reads prefers-reduced-motion once. With it on, a KPI tile prints its value instead of
// counting up from zero when it scrolls into view (jsdom has no IntersectionObserver).
const matchMedia = window.matchMedia;
window.matchMedia = (query: string) => ({
  ...matchMedia(query),
  matches: query.includes("prefers-reduced-motion"),
});

const overview = (extra: Partial<OverviewData> = {}): OverviewData =>
  ({
    status: "ok",
    attention: [],
    kpiList: [
      {
        key: "firing",
        plugin: "alertmanager",
        label: "Alerts firing",
        unit: "",
        value: 2,
        health: "warn",
      },
      {
        key: "vms.running",
        plugin: "proxmox",
        label: "VMs running",
        unit: "",
        value: 5,
        health: "ok",
      },
      {
        key: "vms.total",
        plugin: "proxmox",
        label: "VMs",
        unit: "",
        value: 6,
        health: "ok",
      },
      {
        key: "power",
        plugin: "hardware",
        label: "Power",
        unit: "W",
        value: 240,
        health: "ok",
      },
      {
        key: "cpuTemp",
        plugin: "hardware",
        label: "CPU temp",
        unit: "°C",
        value: 62,
        health: "ok",
      },
    ],
    topology: { host: {}, vms: [], groups: [] },
    kpis: {
      fluxReady: 8,
      fluxTotal: 9,
      fluxSuspended: 0,
      sourceVerified: true,
      nodesReady: 3,
      nodesTotal: 3,
    },
    ...extra,
  }) as unknown as OverviewData;

const app = (name: string, group: string, host = name): AppsData["apps"][number] => ({
  name,
  description: `${name} app`,
  iconUrl: "",
  icon: "",
  ref: "",
  href: `https://${host}.example.com`,
  group: group as AppsData["apps"][number]["group"],
  namespace: "default",
});

const apps = (): AppsData => ({
  apps: [
    app("grafana", "Platform"),
    app("headlamp", "Platform"),
    app("plex", "Apps"),
    app("acme-web", "Tenants"),
  ],
});

const DOWN = {
  name: "kubernetes",
  ok: false,
  error: "unreachable",
  checkedAt: "2026-11-01T00:00:00Z",
};

/** The launcher plugin's links, as its launcher.links envelope carries them. */
const launcher: LauncherData = {
  links: [
    { name: "Proxmox", href: "https://pve.example.com", icon: "", description: "hypervisor" },
    { name: "NAS", href: "https://nas.example.com", icon: "", description: "storage box" },
  ],
};

// What the launcher plugin publishes for an inline list (plugins.launcher.links) and, after a
// reconfigure, for a file (plugins.launcher.file): the payload is the same shape, whichever it came
// from, and a new envelope replaces the list on the page.
const inlineList: LauncherData = {
  links: [
    {
      name: "Inline Proxmox",
      href: "https://pve.example.com",
      icon: "",
      description: "hypervisor",
    },
    { name: "Inline NAS", href: "https://nas.example.com", icon: "", description: "" },
  ],
};
const fileList: LauncherData = {
  links: [
    {
      name: "File Router",
      href: "https://router.example.com",
      icon: "/icons/router.png",
      description: "gateway",
    },
    { name: "File Switch", href: "http://switch.example.com", icon: "", description: "" },
    { name: "File Printer", href: "https://printer.example.com", icon: "", description: "" },
  ],
};

const tileLabels = () =>
  screen
    .getAllByRole("link")
    .map((l) => l.getAttribute("aria-label") ?? "")
    .filter((l) => l.includes("Open"));

describe("pages built from slots", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch({
      "GET /api/v1/me": { user: "dev", role: "admin", scopes: [], ui: { iconBaseUrl: "" } },
    });
    stream = connectStream();
  });
  afterEach(() => stream.close());
  /** The launcher.links envelope. The hub sends it again whenever the launcher row turns visible. */
  const sendLauncher = (data: LauncherData = launcher) => stream.send("launcher.links", data);

  describe("Overview", () => {
    const tiles = [
      "Alerts firing: 2. Open alerts",
      "GitOps 8 of 9 ready. Open GitOps",
      "Nodes 3 of 3 ready. Open cluster",
      "VMs 5 of 6 running. Open VMs",
      "Power 240 W. Open hardware",
      "CPU temperature 62°C. Open hardware",
    ];

    it("draws the six tiles in today's order", () => {
      renderApp(<OverviewPage />);
      stream.plugins(shimRows());
      stream.send("core.overview", overview());
      expect(tileLabels()).toEqual(tiles);
    });

    it("drops Power and CPU temp when the hardware plugin is switched off, and restores them", () => {
      renderApp(<OverviewPage />);
      const rows = shimRows();
      stream.plugins(rows);
      stream.send("core.overview", overview());

      stream.plugins(withState(rows, "hardware", "disabled"));
      expect(tileLabels()).toEqual(tiles.slice(0, 4));

      stream.plugins(rows);
      expect(tileLabels()).toEqual(tiles);
    });

    it("drops a tile with its plugin and keeps the others in place", () => {
      renderApp(<OverviewPage />);
      const rows = shimRows();
      stream.plugins(rows);
      stream.send("core.overview", overview());

      stream.plugins(withState(rows, "proxmox", "unavailable"));
      expect(tileLabels()).toEqual([tiles[0], tiles[1], tiles[2], tiles[4], tiles[5]]);
    });

    it("drops the alerts tile with the alertmanager plugin, and shows none without its KPI", () => {
      renderApp(<OverviewPage />);
      const rows = shimRows();
      stream.plugins(rows);
      stream.send("core.overview", overview());

      stream.plugins(withState(rows, "alertmanager", "disabled"));
      expect(tileLabels()).toEqual(tiles.slice(1));

      stream.plugins(rows);
      // The VM tile draws from its own KPIs: only the alerts KPI goes.
      stream.send("core.overview", overview({ kpiList: overview().kpiList.slice(1) }));
      expect(tileLabels()).toEqual(tiles.slice(1));
    });

    it("adds the KPIs plugins publish after the tiles, and hides them with their plugin", () => {
      renderApp(<OverviewPage />);
      const rows = [...shimRows(), pluginInfo({ id: "queue", name: "Queue" })];
      stream.plugins(rows);
      stream.send(
        "core.overview",
        overview({
          kpiList: [
            {
              key: "depth",
              plugin: "queue",
              label: "Queue depth",
              unit: "msg",
              value: 42,
              health: "ok",
            },
          ],
        }),
      );
      const tile = screen.getByText("Queue depth").closest("[data-slot=panel]");
      expect(tile).toHaveTextContent("42");
      expect(tile).toHaveTextContent("msg");

      stream.plugins(withState(rows, "queue", "disabled"));
      expect(screen.queryByText("Queue depth")).toBeNull();
    });

    it("draws a plugin that has its own tile once, and one without a tile as a generic tile", () => {
      renderApp(<OverviewPage />);
      stream.plugins([...shimRows(), pluginInfo({ id: "queue", name: "Queue" })]);
      const entry = (plugin: string, key: string, label: string, value: number) => ({
        key,
        plugin,
        label,
        unit: "",
        value,
        health: "ok" as const,
      });
      stream.send(
        "core.overview",
        overview({
          kpiList: [
            entry("alertmanager", "firing", "Published alerts", 2),
            entry("hardware", "power", "Published power", 240),
            entry("hardware", "cpuTemp", "Published temperature", 62),
            entry("proxmox", "vms.running", "Published running VMs", 5),
            entry("proxmox", "vms.total", "Published VMs", 6),
            entry("queue", "depth", "Queue depth", 42),
          ],
        }),
      );
      // The alertmanager and hardware modules draw their tiles themselves; their list entries
      // stay unused (one tile per KPI, never a second generic one).
      expect(tileLabels()).toEqual(tiles);
      expect(screen.queryByText("Published alerts")).toBeNull();
      expect(screen.getAllByText("Alerts firing")).toHaveLength(1);
      expect(screen.queryByText("Published power")).toBeNull();
      expect(screen.queryByText("Published temperature")).toBeNull();
      // The queue plugin has no panel: its KPI is the one generic tile, after the six module tiles.
      const tile = screen.getByText("Queue depth").closest("[data-slot=panel]");
      expect(tile).toHaveTextContent("42");
      expect(tile?.parentElement?.children).toHaveLength(7);
    });

    it("lists the launcher's links from an inline list and from a file list", () => {
      renderApp(<OverviewPage />);
      stream.plugins(shimRows());
      stream.send("core.overview", overview());
      stream.send("apps", apps());

      sendLauncher(inlineList);
      const infra = () => screen.getByRole("heading", { name: /^Infrastructure/ });
      expect(infra()).toHaveTextContent("2");
      expect(screen.getByRole("link", { name: /Inline Proxmox/ })).toHaveAttribute(
        "href",
        "https://pve.example.com",
      );
      expect(screen.getByRole("link", { name: /Inline NAS/ })).toBeInTheDocument();

      sendLauncher(fileList);
      expect(infra()).toHaveTextContent("3");
      expect(screen.queryByText("Inline Proxmox")).toBeNull();
      expect(screen.getByRole("link", { name: /File Router/ })).toHaveAttribute(
        "href",
        "https://router.example.com",
      );
      expect(screen.getByRole("link", { name: /File Switch/ })).toHaveAttribute(
        "href",
        "http://switch.example.com",
      );
      // The same-origin icon of the file's first link is drawn; the others are letter tiles.
      expect(
        Array.from(document.querySelectorAll("img")).map((i) => i.getAttribute("src")),
      ).toContain("/icons/router.png");
    });

    it("shows the launcher row with its section counts but announces nothing", () => {
      renderApp(<OverviewPage />);
      stream.plugins(shimRows());
      stream.send("core.overview", overview());
      stream.send("apps", apps());
      sendLauncher();

      expect(screen.getByRole("heading", { name: "Launcher" })).toBeInTheDocument();
      expect(screen.getByRole("heading", { name: /^Platform/ })).toHaveTextContent("2");
      expect(screen.getByRole("heading", { name: /^Infrastructure/ })).toBeInTheDocument();
      expect(screen.queryByRole("status")).toBeNull();
    });

    it("drops the Launcher heading when neither section is on the page, and brings it back", () => {
      renderApp(<OverviewPage />);
      const rows = shimRows();
      stream.plugins(rows);
      stream.send("core.overview", overview());
      stream.send("apps", apps());
      sendLauncher();
      expect(screen.getByRole("heading", { name: "Launcher" })).toBeInTheDocument();

      stream.plugins(withState(withState(rows, "launcher", "disabled"), "kubernetes", "disabled"));
      expect(screen.queryByRole("heading", { name: "Launcher" })).toBeNull();

      stream.plugins(withState(rows, "kubernetes", "disabled"));
      sendLauncher(); // the envelope left with the hidden row: the hub sends it again
      expect(screen.getByRole("heading", { name: "Launcher" })).toBeInTheDocument();
    });

    it("keeps the v0.5.0 spacing and draws the apps' source chips beside the Launcher heading", () => {
      renderApp(<OverviewPage />);
      stream.plugins(shimRows());
      stream.send("core.overview", overview());
      stream.send("apps", apps(), [DOWN]);
      sendLauncher();

      const heading = screen.getByRole("heading", { name: "Launcher" });
      const row = heading.parentElement as HTMLElement;
      expect(row).toHaveTextContent("source unreachable: kubernetes");
      // The chips sit on the heading row and nowhere else on the page.
      expect(screen.getAllByText(/source unreachable: kubernetes/)).toHaveLength(1);
      expect(row.parentElement).toHaveClass("space-y-5");
    });
  });

  describe("Apps", () => {
    const section = (name: RegExp) => {
      const s = screen.getByRole("heading", { name, level: 3 }).closest("section");
      if (!s) throw new Error(`no section ${name}`);
      return s;
    };
    const status = (name: RegExp) => within(section(name)).getByRole("status");

    it("lists the launcher's links from an inline list and from a file list", () => {
      renderApp(<AppsPage />);
      stream.plugins(shimRows());
      stream.send("apps", apps());

      sendLauncher(inlineList);
      const infra = () =>
        screen
          .getByRole("heading", { name: /^Infrastructure/, level: 3 })
          .closest("section") as HTMLElement;
      expect(within(infra()).getByRole("status")).toHaveTextContent("2");
      expect(within(infra()).getByRole("link", { name: /Inline Proxmox/ })).toHaveAttribute(
        "href",
        "https://pve.example.com",
      );

      sendLauncher(fileList);
      expect(within(infra()).getByRole("status")).toHaveTextContent("3");
      expect(within(infra()).queryByText("Inline NAS")).toBeNull();
      expect(within(infra()).getByRole("link", { name: /File Router/ })).toHaveAttribute(
        "href",
        "https://router.example.com",
      );
      expect(within(infra()).getByRole("link", { name: /File Printer/ })).toBeInTheDocument();
      expect(Array.from(infra().querySelectorAll("img")).map((i) => i.getAttribute("src"))).toEqual(
        ["/icons/router.png"],
      );
    });

    it("announces each section's count in its own polite live region", () => {
      renderApp(<AppsPage />);
      stream.plugins(shimRows());
      stream.send("apps", apps());
      sendLauncher();

      // The page-level line is gone; one live region per section.
      expect(screen.getAllByRole("status")).toHaveLength(5); // plus the empty state, which is silent
      expect(status(/^Platform/)).toHaveTextContent("2");
      expect(status(/^Platform/)).toHaveAttribute("aria-live", "polite");
      expect(status(/^Apps/)).toHaveTextContent("1");
      expect(status(/^Tenants/)).toHaveTextContent("1");
      expect(status(/^Infrastructure/)).toHaveTextContent("2");
      expect(status(/^Infrastructure/)).toHaveTextContent(
        "links matching the search in Infrastructure",
      );
    });

    it("filters both sections and updates each count as the query changes", async () => {
      const user = userEvent.setup();
      renderApp(<AppsPage />);
      stream.plugins(shimRows());
      stream.send("apps", apps());
      sendLauncher();
      await screen.findByRole("heading", { name: /^Infrastructure/ });

      await user.type(screen.getByRole("searchbox"), "example.com");
      // Every tile matches on its hostname: counts stay.
      expect(status(/^Platform/)).toHaveTextContent(/^apps matching the search in Platform: 2$/);

      await user.clear(screen.getByRole("searchbox"));
      await user.type(screen.getByRole("searchbox"), "graf");
      expect(status(/^Platform/)).toHaveTextContent("1 of 2");
      // The other sections stay mounted at "0 of N" (hidden from sight; see the persistence tests).
      expect(screen.getAllByRole("status")).toHaveLength(5);
      expect(screen.getByText("grafana")).toBeInTheDocument();
      expect(screen.queryByText("headlamp")).toBeNull();

      await user.clear(screen.getByRole("searchbox"));
      await user.type(screen.getByRole("searchbox"), "pve");
      expect(status(/^Infrastructure/)).toHaveTextContent("1 of 2");
      expect(screen.getByText("Proxmox")).toBeInTheDocument();
      // pve matches the Proxmox link's hostname, so the page does not claim nothing matches.
      expect(screen.queryByText(/No apps match/)).toBeNull();
    });

    it("takes a section's live region away with its plugin", () => {
      renderApp(<AppsPage />);
      const rows = shimRows();
      stream.plugins(rows);
      stream.send("apps", apps());
      sendLauncher();

      // launcher off: the infrastructure section and its live region go.
      stream.plugins(withState(rows, "launcher", "disabled"));
      expect(screen.queryByRole("heading", { name: /^Infrastructure/ })).toBeNull();
      expect(screen.getAllByRole("status")).toHaveLength(4); // three sections and the empty state

      // kubernetes off too: the app sections go with the apps topic; only the page chrome stays.
      stream.plugins(withState(withState(rows, "launcher", "disabled"), "kubernetes", "disabled"));
      expect(screen.getAllByRole("status").map((s) => s.textContent)).toEqual([""]);
      expect(screen.queryByRole("heading", { name: /^Platform/ })).toBeNull();
      expect(screen.getByRole("searchbox")).toBeInTheDocument();

      // launcher back on: its section and live region return on their own.
      stream.plugins(withState(rows, "kubernetes", "disabled"));
      sendLauncher();
      expect(status(/^Infrastructure/)).toHaveTextContent("2");
    });

    describe("live regions across a search", () => {
      const sameElements = (a: HTMLElement[], b: HTMLElement[]) =>
        expect(b.length === a.length && b.every((el, i) => el === a[i])).toBe(true);
      const texts = () => screen.getAllByRole("status").map((s) => s.textContent);

      async function ready() {
        const user = userEvent.setup();
        renderApp(<AppsPage />);
        stream.plugins(shimRows());
        stream.send("apps", apps());
        sendLauncher();
        await screen.findByRole("heading", { name: /^Infrastructure/ });
        return user;
      }

      it("keeps every live region when a search matches nothing, and when it is cleared", async () => {
        const user = await ready();
        const before = screen.getAllByRole("status");
        expect(before).toHaveLength(5); // four sections and the empty state

        await user.type(screen.getByRole("searchbox"), "zzz");
        sameElements(before, screen.getAllByRole("status"));
        expect(texts()).toEqual([
          "apps matching the search in Platform: 0 of 2",
          "apps matching the search in Apps: 0 of 1",
          "apps matching the search in Tenants: 0 of 1",
          "links matching the search in Infrastructure: 0 of 2",
          "No apps match “zzz”.",
        ]);
        // Hidden from sight, not removed: the emptied sections draw no tiles.
        expect(section(/^Platform/)).toHaveClass("sr-only");
        expect(section(/^Platform/).querySelector("a")).toBeNull();
        expect(screen.queryByText("grafana")).toBeNull();

        await user.clear(screen.getByRole("searchbox"));
        sameElements(before, screen.getAllByRole("status"));
        expect(texts()).toEqual([
          "apps matching the search in Platform: 2",
          "apps matching the search in Apps: 1",
          "apps matching the search in Tenants: 1",
          "links matching the search in Infrastructure: 2",
          "",
        ]);
        expect(section(/^Platform/)).not.toHaveClass("sr-only");
        expect(screen.getByText("grafana")).toBeInTheDocument();
      });

      it("keeps a one-item section's live region when its only item is filtered out and back", async () => {
        const user = await ready();
        const before = screen.getAllByRole("status");
        const apps1 = status(/^Apps/);
        expect(apps1).toHaveTextContent(/^apps matching the search in Apps: 1$/);

        await user.type(screen.getByRole("searchbox"), "grafana");
        expect(status(/^Apps/)).toBe(apps1);
        expect(apps1).toHaveTextContent("apps matching the search in Apps: 0 of 1");
        sameElements(before, screen.getAllByRole("status"));
        expect(screen.queryByText("plex")).toBeNull();

        await user.clear(screen.getByRole("searchbox"));
        await user.type(screen.getByRole("searchbox"), "plex");
        expect(status(/^Apps/)).toBe(apps1);
        expect(apps1).toHaveTextContent(/^apps matching the search in Apps: 1$/);
        expect(status(/^Platform/)).toHaveTextContent("0 of 2");
        expect(screen.getByText("plex")).toBeInTheDocument();
        sameElements(before, screen.getAllByRole("status"));
      });

      it("makes the count one atomic announcement", async () => {
        await ready();
        for (const s of screen.getAllByRole("status").slice(0, 4)) {
          expect(s).toHaveAttribute("aria-atomic", "true");
        }
      });

      it("does not say nothing matches while a section still has a match", async () => {
        const user = await ready();
        // Only the Proxmox infrastructure tile matches: no apps do, but something is shown.
        await user.type(screen.getByRole("searchbox"), "hypervisor");
        expect(status(/^Infrastructure/)).toHaveTextContent("1 of 2");
        expect(screen.getByText("Proxmox")).toBeInTheDocument();
        expect(screen.queryByText(/No apps match/)).toBeNull();
        expect(screen.getAllByRole("status").at(-1)).toHaveTextContent(/^$/);

        // And the other way round: an app matches, no link does.
        await user.clear(screen.getByRole("searchbox"));
        await user.type(screen.getByRole("searchbox"), "grafana");
        expect(screen.queryByText(/No apps match/)).toBeNull();
      });

      it("stays silent about matches when nothing is listed at all", async () => {
        const user = userEvent.setup();
        renderApp(<AppsPage />);
        stream.plugins(shimRows());
        stream.send("apps", { apps: [] });
        sendLauncher({ links: [] });
        await user.type(screen.getByRole("searchbox"), "zzz");
        expect(screen.queryByText(/No apps match/)).toBeNull();
      });
      it("stays silent while the apps topic is available but its data has not arrived", async () => {
        const user = userEvent.setup();
        renderApp(<AppsPage />);
        stream.plugins(shimRows()); // kubernetes is on; no apps envelope yet
        sendLauncher();
        await user.type(screen.getByRole("searchbox"), "zzz"); // matches no infrastructure link
        expect(screen.queryByText(/No apps match/)).toBeNull();

        stream.send("apps", apps());
        expect(screen.getByText("No apps match “zzz”.")).toBeInTheDocument();
      });

      it("stays silent while launcher.links has not arrived, and speaks once it has", async () => {
        const user = userEvent.setup();
        renderApp(<AppsPage />);
        stream.plugins(shimRows());
        stream.send("apps", apps());
        await user.type(screen.getByRole("searchbox"), "zzz");
        expect(screen.queryByText(/No apps match/)).toBeNull(); // the links are not known yet

        sendLauncher();
        expect(screen.getByText("No apps match “zzz”.")).toBeInTheDocument();
      });
    });

    describe("with no section on the page", () => {
      const off = (rows = shimRows()) =>
        withState(withState(rows, "launcher", "disabled"), "kubernetes", "unavailable");

      it("explains why instead of showing only the heading and the search", () => {
        renderApp(<AppsPage />);
        stream.plugins(off());
        expect(screen.getByRole("heading", { name: "Apps" })).toBeInTheDocument();
        expect(screen.getByText(/Nothing to launch/)).toHaveTextContent(
          "the kubernetes plugin (the apps) is unavailable, and the launcher plugin (the infrastructure links) is disabled.",
        );
      });

      it("says the launcher file is empty when its plugin is on but lists no links", () => {
        renderApp(<AppsPage />);
        stream.plugins(withState(shimRows(), "kubernetes", "disabled"));
        sendLauncher({ links: [] });
        expect(screen.getByText(/Nothing to launch/)).toHaveTextContent(
          "and the launcher plugin (the infrastructure links) lists no links.",
        );
      });

      it("says nothing while the plugin list is loading, and goes away when a plugin returns", () => {
        stubFetch({ "GET /api/v1/plugins": () => new Promise(() => {}) });
        renderApp(<AppsPage />);
        expect(screen.queryByText(/Nothing to launch/)).toBeNull();

        const rows = off();
        stream.plugins(rows);
        expect(screen.getByText(/Nothing to launch/)).toBeInTheDocument();

        stream.plugins(withState(rows, "launcher", "healthy"));
        sendLauncher();
        expect(screen.queryByText(/Nothing to launch/)).toBeNull();
        expect(screen.getByRole("heading", { name: /^Infrastructure/ })).toBeInTheDocument();
      });
    });

    describe("with a launcher that failed to start", () => {
      // A failed start publishes nothing and the topic never arrives: that is an answer.
      it("still says No apps match when the apps are listed", async () => {
        const user = userEvent.setup();
        renderApp(<AppsPage />);
        stream.plugins(withState(shimRows(), "launcher", "error"));
        stream.send("apps", apps());
        await user.type(screen.getByRole("searchbox"), "zzz");
        expect(screen.getByText("No apps match “zzz”.")).toBeInTheDocument();
      });

      it("says Nothing to launch, naming the error, instead of staying blank", () => {
        renderApp(<AppsPage />);
        stream.plugins(
          withState(withState(shimRows(), "launcher", "error"), "kubernetes", "disabled"),
        );
        expect(screen.getByText(/Nothing to launch/)).toHaveTextContent(
          "and the launcher plugin (the infrastructure links) is error.",
        );
      });
    });

    it("shows placeholder tiles until the apps topic arrives", () => {
      renderApp(<AppsPage />);
      stream.plugins(shimRows());
      expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    });

    it("shows the apps' source chips once, above the sections", () => {
      renderApp(<AppsPage />);
      stream.plugins(shimRows());
      stream.send("apps", apps(), [DOWN]);
      expect(screen.getAllByText(/source unreachable: kubernetes/)).toHaveLength(1);
    });
  });
});
