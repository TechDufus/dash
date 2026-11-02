import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ActionInfo, FluxData, Me, PluginInfo, PodDetail } from "@/api/types";
import { ui } from "@/api/ui-store";
import { GitOpsPage } from "@/pages/gitops";
import { PodSheet } from "@/pages/pod-sheet";
import { AlertsPage } from "@/plugins/modules/alertmanager/alerts-page";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { type RenderAppResult, renderApp, stubFetch } from "@/test/harness";
import { shimRows } from "@/test/shims";
import { CommandPalette } from "./command-palette";

// P4.2 at shell level: the role /me reports decides, on every page that offers an action, whether
// the action control is drawn. useActionAvailable implements the rule; these tests pin that every
// surface goes through it, with the role as the only difference between the two runs.

const action = (name: string): ActionInfo => ({
  name,
  title: name,
  destructive: false,
  idempotent: false,
  confirm: "none",
  minRole: "admin",
  inputSchema: null,
});

/** The built-in rows, with every action of the surfaces below listed and admin-only. */
function rows(): PluginInfo[] {
  return shimRows().map((p) =>
    p.id === "alertmanager"
      ? { ...p, actions: [action("silence.create"), action("silence.expire")] }
      : p,
  );
}

const me = (role: string) => ({
  user: "u",
  role,
  authMode: "proxy",
  scopes: ["read"],
  ui: { iconBaseUrl: "" },
});

const alertsData = {
  alerts: [
    {
      ref: "alertmanager:Alert/f1",
      protected: false,
      fingerprint: "f1",
      name: "DiskFull",
      severity: "warning",
      state: "active",
      summary: "DiskFull summary",
      description: "",
      labels: { alertname: "DiskFull", severity: "warning" },
      startsAt: "2026-01-01T00:00:00Z",
      silencedBy: [],
      inhibitedBy: [],
    },
  ],
  silences: [
    {
      ref: "alertmanager:Silence/s1",
      dash: true,
      id: "s1",
      matchers: [{ name: "alertname", value: "Muted", isRegex: false, isEqual: true }],
      createdBy: "dash:alice",
      comment: "ours",
      startsAt: "2026-01-01T00:00:00Z",
      endsAt: "2026-01-02T00:00:00Z",
      state: "active",
    },
  ],
  silenceDurations: ["1h", "4h"],
};

const pod = {
  pod: {
    namespace: "media",
    name: "plex-0",
    phase: "Running",
    reason: "",
    health: "ok",
    node: "node-a",
    readyContainers: 1,
    restarts: 0,
  },
  ips: [],
  conditions: [],
  containers: [],
  ownerChain: [{ kind: "Deployment", name: "plex" }],
  events: [],
} as unknown as PodDetail;

const fluxObj = (kind: string, namespace: string, name: string, over = {}) => ({
  ref: `flux:${kind}/${namespace}/${name}`,
  kind,
  namespace,
  name,
  ready: true,
  suspended: false,
  suspendReason: "",
  revision: "main@sha1:0123456789abcdef",
  message: "",
  specVerify: true,
  verified: true,
  links: [],
  ...over,
});

const flux = {
  sources: [fluxObj("GitRepository", "gitops", "root")],
  kustomizations: [fluxObj("Kustomization", "gitops", "root", { specVerify: false })],
  helmReleases: [],
  verifiedSources: [{ kind: "GitRepository", namespace: "gitops", name: "root" }],
} as unknown as FluxData;

/** One place that draws action controls: how to show it, and the action controls it drew. */
interface Surface {
  name: string;
  /** Renders it; the result's QueryClient lets the test wait for /me. */
  show(): RenderAppResult;
  /** The action controls on screen once the surface has settled. */
  controls(): Promise<HTMLElement[]>;
  /** How many controls an admin sees (the positive control). */
  admin: number;
}

const surfaces: Surface[] = [
  {
    name: "the Alerts page (Silence and Expire)",
    show: () => renderApp(<AlertsPage />, { path: "/alerts" }),
    async controls() {
      await screen.findByText("DiskFull");
      const silence = screen.queryAllByRole("button", { name: "Silence" });
      await userEvent.click(screen.getByRole("tab", { name: /Silences/ }));
      await screen.findByText("ours");
      return [...silence, ...screen.queryAllByRole("button", { name: "Expire" })];
    },
    admin: 2,
  },
  {
    name: "the pod sheet (Restart)",
    show: () => renderApp(<PodSheet ns="media" name="plex-0" />),
    async controls() {
      await screen.findByText("Owner");
      return screen.queryAllByRole("button", { name: /^Restart/ });
    },
    admin: 1,
  },
  {
    name: "the GitOps page (Reconcile on banners and rows)",
    show: () => renderApp(<GitOpsPage />, { path: "/gitops" }),
    async controls() {
      await screen.findByText("Flux objects");
      return screen.queryAllByRole("button", { name: /^Reconcile/ });
    },
    // The banner of the one verified source, and the one Kustomization row.
    admin: 2,
  },
  {
    name: "the command palette (Actions group)",
    show: () => {
      // The palette reads /me only while it is open.
      act(() => ui.setPalette(true));
      return renderApp(<CommandPalette />);
    },
    async controls() {
      const dialog = await screen.findByRole("dialog");
      await act(async () => {});
      return [
        ...dialog.querySelectorAll<HTMLElement>("[cmdk-group-heading]"),
        ...dialog.querySelectorAll<HTMLElement>("[cmdk-item]"),
      ].filter((el) => /^(Actions|Restart |Reconcile )/.test(el.textContent ?? ""));
    },
    // The Actions heading, one Reconcile item and one Restart item.
    admin: 3,
  },
];

describe("the shell offers actions by the role of /me", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => {
    act(() => ui.setPalette(false));
    stream.close();
  });

  /** Renders `surface` as `role`, with /me read into the query cache and every topic delivered. */
  async function open(surface: Surface, role: string) {
    const stub = stubFetch({
      "GET /api/v1/me": me(role),
      "GET /api/v1/plugins/kubernetes/pods/media/plex-0": pod,
    });
    const view = surface.show();
    stream.plugins(rows());
    stream.send("alertmanager.alerts", alertsData);
    stream.send("flux.objects", flux);
    stream.send("kubernetes.cluster", {
      nodes: [],
      workloads: [
        {
          kind: "Deployment",
          namespace: "media",
          name: "plex",
          restartable: true,
          protected: false,
        },
      ],
      pods: [],
    });
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    // The role is what the gate reads: wait on the published state, not on the request.
    await waitFor(() => expect(view.queryClient.getQueryData<Me>(["me"])?.role).toBe(role));
    await act(async () => {});
  }

  describe("TestViewerSeesNoActionButtons", () => {
    it.each(surfaces)("$name draws no action control for a viewer", async (surface) => {
      await open(surface, "viewer");
      expect(await surface.controls()).toHaveLength(0);
    });
  });

  describe("the same surfaces for an admin (positive control)", () => {
    it.each(surfaces)("$name draws its action controls", async (surface) => {
      await open(surface, "admin");
      await waitFor(async () => expect(await surface.controls()).toHaveLength(surface.admin));
    });
  });
});
