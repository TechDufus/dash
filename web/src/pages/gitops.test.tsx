import { readFileSync } from "node:fs";
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type {
  ActionInfo,
  FluxData,
  FluxKind,
  FluxObj,
  FluxSourceRef,
  PluginInfo,
} from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { GitOpsPage } from "./gitops";

const RECONCILE = "POST /api/v1/plugins/flux/actions/reconcile";

const reconcile = (minRole = "admin"): ActionInfo => ({
  name: "reconcile",
  title: "Reconcile",
  destructive: false,
  idempotent: true,
  confirm: "none",
  minRole,
  inputSchema: null,
});

const row = (actions: ActionInfo[] = [reconcile()], state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "flux",
    state,
    topics: ["flux.objects"],
    actions,
    nav: [{ path: "/gitops", label: "GitOps", title: "GitOps (Flux)", icon: "GitBranch" }],
  });

const obj = (
  kind: FluxObj["kind"],
  namespace: string,
  name: string,
  over: Partial<FluxObj> = {},
): FluxObj => ({
  ref: `flux:${kind}/${namespace}/${name}`,
  kind,
  namespace,
  name,
  ready: true,
  suspended: false,
  suspendReason: "",
  revision: "main@sha1:0123456789abcdef",
  message: "",
  specVerify: false,
  links: [],
  ...over,
});

const ref = (kind: FluxKind, namespace: string, name: string): FluxSourceRef => ({
  kind,
  namespace,
  name,
});

const signed = { specVerify: true, verified: true };

const data = (over: Partial<FluxData> = {}): FluxData => ({
  sources: [
    obj("GitRepository", "gitops", "root", signed),
    obj("OCIRepository", "tenant", "app", { specVerify: true, verified: false }),
    obj("GitRepository", "gitops", "pinned", { specVerify: false }),
    obj("GitRepository", "gitops", "unsure", { specVerify: true }),
  ],
  kustomizations: [obj("Kustomization", "gitops", "root")],
  helmReleases: [
    obj("HelmRelease", "media", "player", { ready: false, message: "install failed" }),
  ],
  verifiedSources: [
    ref("GitRepository", "gitops", "pinned"),
    ref("GitRepository", "gitops", "root"),
    ref("GitRepository", "gitops", "unsure"),
    ref("OCIRepository", "tenant", "app"),
  ],
  ...over,
});

const me = (role: string) => ({ user: "u", role, scopes: ["read"], ui: { iconBaseUrl: "" } });

describe("the GitOps page", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => stream.close());

  async function show(role: string, rows: PluginInfo[], payload: FluxData | null = data()) {
    const stub = stubFetch({ "GET /api/v1/me": me(role), [RECONCILE]: { status: "accepted" } });
    renderApp(<GitOpsPage />, { path: "/gitops" });
    stream.plugins(rows);
    if (payload) stream.send("flux.objects", payload);
    if (payload) await screen.findByText("Flux objects");
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    await act(async () => {}); // /me has been read into the query cache
    return stub;
  }

  // The panel title is an h2 too; a source banner is every other one.
  const banners = () =>
    screen.queryAllByRole("heading", { level: 2 }).filter((h) => h.textContent !== "Flux objects");
  const reconcileButtons = () => screen.queryAllByRole("button", { name: /^Reconcile/ });

  it("shows a skeleton and no header until the topic arrives", async () => {
    await show("admin", [row()], null);
    expect(banners()).toHaveLength(0);
    expect(screen.queryByText("Flux objects")).toBeNull();
  });

  it("lists every verified source in the header, in the order the plugin sends them", async () => {
    await show("admin", [row()]);
    expect(banners().map((h) => h.textContent)).toEqual(["pinned", "root", "unsure", "app"]);
    expect(screen.getByText("OCIRepository · tenant")).toBeTruthy();
  });

  it("says what each source's verification is", async () => {
    await show("admin", [row()]);
    expect(screen.getAllByText("Source verified: signature checked")).toHaveLength(1);
    expect(screen.getAllByText("Source NOT verified: signature check failed")).toHaveLength(1);
    expect(
      screen.getAllByText("Source verification missing: no SourceVerified condition"),
    ).toHaveLength(1);
    expect(screen.getAllByText("Source is not signed: spec.verify is not set")).toHaveLength(1);
  });

  it("reports a verified source the topic does not list, from the data", async () => {
    await show(
      "admin",
      [row()],
      data({
        sources: [],
        verifiedSources: [ref("OCIRepository", "tenant", "ghost")],
      }),
    );
    expect(screen.getByText("OCIRepository tenant/ghost not found")).toBeTruthy();
    expect(banners().map((h) => h.textContent)).toEqual(["ghost"]);
    // There is nothing to reconcile: the cluster would answer 404.
    expect(screen.queryAllByRole("button", { name: "Reconcile" })).toHaveLength(0);
  });

  it("draws no header when the plugin verifies no source", async () => {
    await show("admin", [row()], data({ verifiedSources: [] }));
    expect(banners()).toHaveLength(0);
    expect(screen.getByText("Flux objects")).toBeTruthy();
  });

  it("offers Reconcile in the header and on every row to an admin, and posts to the action", async () => {
    const stub = await show("admin", [row()]);
    // Four banners, and one icon button on the one Kustomization row.
    await waitFor(() =>
      expect(screen.getAllByRole("button", { name: "Reconcile" })).toHaveLength(4),
    );
    expect(
      screen.getByRole("button", { name: "Reconcile Kustomization gitops/root" }),
    ).toBeTruthy();

    const header = banners()[3]?.closest(".p-4") as HTMLElement;
    await userEvent.click(within(header).getByRole("button", { name: "Reconcile" }));
    await waitFor(() => expect(stub.callsTo(RECONCILE)).toHaveLength(1));
    expect(stub.callsTo(RECONCILE)[0]?.body).toEqual({
      kind: "OCIRepository",
      namespace: "tenant",
      name: "app",
    });
    expect(stub.calls.filter((c) => c.path === "/api/actions/reconcile")).toEqual([]);

    await userEvent.click(
      screen.getByRole("button", { name: "Reconcile Kustomization gitops/root" }),
    );
    await waitFor(() => expect(stub.callsTo(RECONCILE)).toHaveLength(2));
    expect(stub.callsTo(RECONCILE)[1]?.body).toEqual({
      kind: "Kustomization",
      namespace: "gitops",
      name: "root",
    });
  });

  it("offers no Reconcile to a viewer, though the row lists the action", async () => {
    await show("viewer", [row()]);
    expect(reconcileButtons()).toHaveLength(0);
    expect(banners()).toHaveLength(4);
  });

  it("offers no Reconcile while the row lists no action (actions.enabled is off)", async () => {
    await show("admin", [row([])]);
    expect(reconcileButtons()).toHaveLength(0);
  });

  it("follows a live switch of actions.enabled", async () => {
    await show("admin", [row([])]);
    expect(reconcileButtons()).toHaveLength(0);
    stream.plugins([row()]);
    await waitFor(() => expect(reconcileButtons().length).toBeGreaterThan(0));
    stream.plugins([row([])]);
    await waitFor(() => expect(reconcileButtons()).toHaveLength(0));
  });

  it("carries no lab name: nothing on the page comes from a literal", async () => {
    await show("admin", [row()]);
    expect(document.body.textContent).not.toContain("flux-system");
  });
});

describe("the flux web sources", () => {
  it.each([
    "./gitops.tsx",
    "../plugins/modules/flux/index.tsx",
    "../plugins/modules/flux/panels.tsx",
    "../plugins/modules/flux/actions.ts",
  ])("%s has no flux-system literal", (file) => {
    const text = readFileSync(new URL(file, import.meta.url), "utf8");
    expect(text).not.toContain("flux-system");
  });
});
