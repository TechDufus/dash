import { QueryClient } from "@tanstack/react-query";
import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AppsData, OverviewData, StorageData } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp as renderWith, stubFetch } from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
import { AppsPage } from "./apps";
import { OverviewPage } from "./overview";
import { StoragePage } from "./storage";

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
    kpiList: [],
    topology: { host: {}, vms: [], groups: [] },
    kpis: {
      alertsFiring: 2,
      fluxReady: 8,
      fluxTotal: 9,
      fluxSuspended: 0,
      sourceVerified: true,
      nodesReady: 3,
      nodesTotal: 3,
      vmsRunning: 5,
      vmsTotal: 6,
      powerWatts: 240,
      cpuTempMaxC: 62,
    },
    ...extra,
  }) as unknown as OverviewData;

const storage = (): StorageData =>
  ({
    pvcs: [],
    buckets: [],
    rustfs: { usedBytes: 1, usableBytes: 2 },
    pveStorages: [{ name: "local-zfs", usedBytes: 1, sizeBytes: 2 }],
    cnpg: [],
    nasBackups: [],
    etcd: { dbBytes: 1, quotaBytes: 2 },
  }) as StorageData;

const app = (name: string, group: string, host = name): AppsData["apps"][number] => ({
  name,
  description: `${name} app`,
  iconUrl: "",
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

const config = {
  hostModel: "",
  launcher: [
    { name: "Proxmox", href: "https://pve.example.com", icon: "", description: "hypervisor" },
    { name: "NAS", href: "https://nas.example.com", icon: "", description: "storage box" },
  ],
};

/** renderApp with the UI config (the launcher file's links) already in the query cache. */
function renderApp(ui: ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  queryClient.setQueryData(["config"], config);
  return renderWith(ui, { queryClient });
}

const tileLabels = () =>
  screen
    .getAllByRole("link")
    .map((l) => l.getAttribute("aria-label") ?? "")
    .filter((l) => l.includes("Open"));

describe("pages built from slots", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch({ "GET /api/config": config });
    stream = connectStream();
  });
  afterEach(() => stream.close());

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

    it("shows the launcher row with its section counts but announces nothing", () => {
      renderApp(<OverviewPage />);
      stream.plugins(shimRows());
      stream.send("core.overview", overview());
      stream.send("apps", apps());

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
      expect(screen.getByRole("heading", { name: "Launcher" })).toBeInTheDocument();

      stream.plugins(withState(withState(rows, "launcher", "disabled"), "kubernetes", "disabled"));
      expect(screen.queryByRole("heading", { name: "Launcher" })).toBeNull();

      stream.plugins(withState(rows, "kubernetes", "disabled"));
      expect(screen.getByRole("heading", { name: "Launcher" })).toBeInTheDocument();
    });

    it("keeps the v0.5.0 spacing and draws the apps' source chips beside the Launcher heading", () => {
      renderApp(<OverviewPage />);
      stream.plugins(shimRows());
      stream.send("core.overview", overview());
      stream.send("apps", apps(), [DOWN]);

      const heading = screen.getByRole("heading", { name: "Launcher" });
      const row = heading.parentElement as HTMLElement;
      expect(row).toHaveTextContent("source unreachable: kubernetes");
      // The chips sit on the heading row and nowhere else on the page.
      expect(screen.getAllByText(/source unreachable: kubernetes/)).toHaveLength(1);
      expect(row.parentElement).toHaveClass("space-y-5");
    });
  });

  describe("Storage", () => {
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
      stream.plugins(shimRows());
      stream.send("storage", storage());

      expect(panelNames()).toEqual(names);
      const cell = (name: string) =>
        screen.getByRole("heading", { name }).closest("div.lg\\:col-span-2");
      expect(cell("Persistent volumes")).not.toBeNull();
      for (const half of names.slice(1)) expect(cell(half), half).toBeNull();
    });

    it("drops the Proxmox panel when proxmox is off", () => {
      renderApp(<StoragePage />);
      const rows = shimRows();
      stream.plugins(rows);
      stream.send("storage", storage());

      stream.plugins(withState(rows, "proxmox", "disabled"));
      expect(panelNames()).toEqual(names.filter((n) => n !== "Proxmox storage"));
    });

    it("shows one explanation when kubernetes, the source of the storage topic, is off", () => {
      renderApp(<StoragePage />);
      const rows = shimRows();
      stream.plugins(rows);
      stream.send("storage", storage());

      stream.plugins(withState(rows, "kubernetes", "disabled"));
      expect(screen.queryAllByRole("heading", { level: 2 })).toEqual([]);
      expect(
        screen.getByText("Storage data comes from the kubernetes plugin, which is disabled."),
      ).toBeInTheDocument();
      expect(document.querySelector("[aria-busy=true]")).toBeNull();
    });

    it("shows the skeleton until the storage topic arrives", () => {
      renderApp(<StoragePage />);
      stream.plugins(shimRows());
      expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
      expect(screen.queryAllByRole("heading", { level: 2 })).toEqual([]);
    });
  });

  describe("Apps", () => {
    const section = (name: RegExp) => {
      const s = screen.getByRole("heading", { name, level: 3 }).closest("section");
      if (!s) throw new Error(`no section ${name}`);
      return s;
    };
    const status = (name: RegExp) => within(section(name)).getByRole("status");

    it("announces each section's count in its own polite live region", () => {
      renderApp(<AppsPage />);
      stream.plugins(shimRows());
      stream.send("apps", apps());

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
        const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
        queryClient.setQueryData(["config"], { hostModel: "", launcher: [] });
        renderWith(<AppsPage />, { queryClient });
        stream.plugins(shimRows());
        stream.send("apps", { apps: [] });
        await user.type(screen.getByRole("searchbox"), "zzz");
        expect(screen.queryByText(/No apps match/)).toBeNull();
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
