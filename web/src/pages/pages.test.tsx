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
      expect(screen.getAllByRole("status")).toHaveLength(4);
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
      expect(screen.queryByRole("heading", { name: /^Apps/, level: 3 })).toBeNull();
      expect(screen.queryByRole("heading", { name: /^Tenants/ })).toBeNull();
      expect(screen.queryByRole("heading", { name: /^Infrastructure/ })).toBeNull();
      expect(screen.getAllByRole("status")).toHaveLength(1);
      expect(screen.getByText("grafana")).toBeInTheDocument();
      expect(screen.queryByText("headlamp")).toBeNull();

      await user.clear(screen.getByRole("searchbox"));
      await user.type(screen.getByRole("searchbox"), "pve");
      expect(status(/^Infrastructure/)).toHaveTextContent("1 of 2");
      expect(screen.getByText("Proxmox")).toBeInTheDocument();
      // Apps that match nothing say so, in a live region of their own.
      expect(screen.getByText("No apps match “pve”.")).toHaveAttribute("role", "status");
    });

    it("takes a section's live region away with its plugin", () => {
      renderApp(<AppsPage />);
      const rows = shimRows();
      stream.plugins(rows);
      stream.send("apps", apps());

      // launcher off: the infrastructure section and its live region go.
      stream.plugins(withState(rows, "launcher", "disabled"));
      expect(screen.queryByRole("heading", { name: /^Infrastructure/ })).toBeNull();
      expect(screen.getAllByRole("status")).toHaveLength(3);

      // kubernetes off too: the app sections go with the apps topic; only the page chrome stays.
      stream.plugins(withState(withState(rows, "launcher", "disabled"), "kubernetes", "disabled"));
      expect(screen.queryAllByRole("status")).toEqual([]);
      expect(screen.queryByRole("heading", { name: /^Platform/ })).toBeNull();
      expect(screen.getByRole("searchbox")).toBeInTheDocument();

      // launcher back on: its section and live region return on their own.
      stream.plugins(withState(rows, "kubernetes", "disabled"));
      expect(status(/^Infrastructure/)).toHaveTextContent("2");
    });

    it("shows placeholder tiles until the apps topic arrives", () => {
      renderApp(<AppsPage />);
      stream.plugins(shimRows());
      expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    });
  });
});
