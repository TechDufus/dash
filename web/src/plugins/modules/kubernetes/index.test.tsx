import { act, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import type { ActionInfo, ClusterData, PluginInfo } from "@/api/types";
import { ui } from "@/api/ui-store";
import { CommandPalette } from "@/shell/command-palette";
import {
  envelope,
  type FakeEventSource,
  type FetchStub,
  installEventSource,
  pluginInfo,
  pluginsEnvelope,
  renderApp,
  stubFetch,
} from "@/test/harness";
import type { PaletteCtx } from "../../types";
import kubernetes from "./index";

const cluster = {
  nodes: [{ name: "node-a", ip: "192.0.2.10" }],
  workloads: [
    { kind: "Deployment", namespace: "media", name: "app", restartable: true, protected: false },
    {
      kind: "Deployment",
      namespace: "kube-system",
      name: "dns",
      restartable: false,
      protected: true,
    },
  ],
  pods: [{ namespace: "media", name: "app-0" }],
} as unknown as ClusterData;

const apps = { apps: [{ name: "Wiki", href: "https://wiki.example.com", group: "docs" }] };

function ctxWith(available: boolean, appsData: typeof apps = apps) {
  const calls: string[] = [];
  const ctx: PaletteCtx = {
    topic: ((name: string) =>
      name === "kubernetes.cluster"
        ? cluster
        : name === "kubernetes.apps"
          ? appsData
          : undefined) as PaletteCtx["topic"],
    go: (to) => calls.push(to),
    close: vi.fn(),
    actionAvailable: (id, action) => {
      calls.push(`available:${id}/${action}`);
      return available;
    },
  };
  return { ctx, calls };
}

describe("the kubernetes module", () => {
  it("declares the plugin's three topics under their plugin-prefixed names", () => {
    expect(Object.keys(kubernetes.topics).sort()).toEqual([
      "kubernetes.apps",
      "kubernetes.cluster",
      "kubernetes.storage",
    ]);
  });

  it("asks whether kubernetes may restart, and offers Restart on restartable workloads only", () => {
    const { ctx, calls } = ctxWith(true);
    const groups = kubernetes.palette?.(ctx) ?? [];
    expect(calls).toContain("available:kubernetes/restart");
    const actions = groups.find((g) => g.id === "actions");
    expect(actions?.items.map((i) => i.label)).toEqual(["Restart media/app"]);
  });

  it("offers no Restart item when the action is not available, and keeps every other group", () => {
    const { ctx } = ctxWith(false);
    const groups = kubernetes.palette?.(ctx) ?? [];
    expect(groups.find((g) => g.id === "actions")?.items).toEqual([]);
    expect(groups.filter((g) => g.items.length > 0).map((g) => g.id)).toEqual([
      "nodes",
      "workloads",
      "pods",
      "apps",
    ]);
  });

  it("opens an app in a new tab with no opener, and navigates the other items", () => {
    const { ctx, calls } = ctxWith(true);
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    try {
      const groups = kubernetes.palette?.(ctx) ?? [];
      const item = (group: string) => groups.find((g) => g.id === group)?.items[0];
      item("pods")?.onSelect();
      item("nodes")?.onSelect();
      item("apps")?.onSelect();
      expect(calls).toContain("/cluster/pods/media/app-0");
      expect(calls).toContain("/cluster?tab=nodes");
      expect(open).toHaveBeenCalledWith(
        "https://wiki.example.com",
        "_blank",
        "noopener,noreferrer",
      );
    } finally {
      open.mockRestore();
    }
  });

  it.each([
    "javascript:alert(1)",
    "  JaVaScRiPt:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "vbscript:msgbox(1)",
    "http://[",
  ])("does not open an app whose href is %j", (href) => {
    const { ctx, calls } = ctxWith(true, { apps: [{ name: "Evil", href, group: "docs" }] });
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    try {
      const groups = kubernetes.palette?.(ctx) ?? [];
      const item = groups.find((g) => g.id === "apps")?.items[0];
      expect(item?.label).toBe("Evil");
      item?.onSelect();
      expect(open).not.toHaveBeenCalled();
      expect(calls).toEqual(["available:kubernetes/restart"]);
    } finally {
      open.mockRestore();
    }
  });
});

describe("the kubernetes items of the command palette", () => {
  let ES: typeof FakeEventSource;
  let me: FetchStub;

  const restart = (minRole: string): ActionInfo => ({
    name: "restart",
    title: "Restart",
    destructive: true,
    idempotent: false,
    confirm: "none",
    minRole,
    inputSchema: null,
  });
  const row = (actions: ActionInfo[], state = "healthy"): PluginInfo =>
    pluginInfo({
      id: "kubernetes",
      name: "Kubernetes",
      state: state as never,
      topics: ["kubernetes.cluster", "kubernetes.storage", "kubernetes.apps"],
      actions,
    });

  beforeEach(() => {
    ES = installEventSource();
  });
  afterEach(() => {
    act(() => ui.setPalette(false));
    stopStream();
  });

  async function show(role: string, rows: PluginInfo[]) {
    me = stubFetch({
      "GET /api/v1/plugins": () => new Promise(() => {}),
      "GET /api/v1/me": { user: "u", role, scopes: ["read"], ui: { iconBaseUrl: "" } },
    });
    startStream();
    const es = ES.instances.at(-1);
    if (!es) throw new Error("no EventSource");
    es.open();
    es.emit("core.plugins", pluginsEnvelope(rows));
    es.emit("kubernetes.cluster", envelope("kubernetes.cluster", cluster));
    renderApp(<CommandPalette />);
    act(() => ui.setPalette(true));
    const dialog = await screen.findByRole("dialog");
    await waitFor(() => expect(me.callsTo("GET /api/v1/me")).toHaveLength(1));
    await act(async () => {}); // /me has been read into the query cache
    return { dialog, es };
  }

  const restartItems = (dialog: HTMLElement) =>
    within(dialog)
      .queryAllByText(/^Restart /)
      .map((e) => e.textContent);

  it("lists Restart for an admin while the row lists the action", async () => {
    const { dialog } = await show("admin", [row([restart("admin")])]);
    expect(restartItems(dialog)).toEqual(["Restart media/app"]);
  });

  const hidden: [string, string, PluginInfo][] = [
    ["a viewer, though the row lists the action", "viewer", row([restart("admin")])],
    ["a row that lists no action", "admin", row([])],
  ];
  for (const [what, role, r] of hidden) {
    it(`lists no Restart for ${what}`, async () => {
      const { dialog } = await show(role, [r]);
      expect(restartItems(dialog)).toEqual([]);
      // The workload itself is still searchable.
      expect(within(dialog).getByText("media/app")).toBeInTheDocument();
    });
  }

  it("follows a live switch of actions.enabled while the palette is open", async () => {
    const { dialog, es } = await show("admin", [row([])]);
    expect(restartItems(dialog)).toEqual([]);
    act(() => es.emit("core.plugins", pluginsEnvelope([row([restart("admin")])])));
    expect(restartItems(dialog)).toEqual(["Restart media/app"]);
    act(() => es.emit("core.plugins", pluginsEnvelope([row([])])));
    expect(restartItems(dialog)).toEqual([]);
  });
});
