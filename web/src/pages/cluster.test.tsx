import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import type { ActionInfo, ClusterData, PluginInfo, Workload } from "@/api/types";
import { ui } from "@/api/ui-store";
import { GlobalDialogs } from "@/components/dialogs";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { ClusterPage } from "./cluster";

const restartAction = (minRole = "admin"): ActionInfo => ({
  name: "restart",
  title: "Restart",
  destructive: true,
  idempotent: false,
  confirm: "none",
  minRole,
  inputSchema: null,
});

const kubernetes = (actions: ActionInfo[] = [restartAction()], state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "kubernetes",
    name: "Kubernetes",
    state: state as never,
    topics: ["kubernetes.cluster", "kubernetes.storage", "kubernetes.apps"],
    nav: [{ path: "/cluster", label: "Cluster", title: "Kubernetes cluster", icon: "Boxes" }],
    actions,
  });

const workload = (name: string, over: Partial<Workload> = {}): Workload =>
  ({
    ref: `kubernetes:Workload/Deployment/media/${name}`,
    kind: "Deployment",
    namespace: "media",
    name,
    ready: 1,
    desired: 1,
    images: ["registry.example.com/app:1"],
    health: "ok",
    restartable: true,
    protected: false,
    links: [],
    ...over,
  }) as Workload;

const cluster = (): ClusterData =>
  ({
    nodes: [
      { ref: "kubernetes:Node/node-a", name: "node-a", ip: "192.0.2.10", ready: true },
      { ref: "kubernetes:Node/node-b", name: "node-b", ip: "192.0.2.11", ready: false },
    ],
    namespaces: [{ ref: "kubernetes:Namespace/media", name: "media" }],
    workloads: [
      workload("plex"),
      workload("job-runner", { kind: "Job" as never, restartable: false }),
    ],
    pods: [
      { ref: "kubernetes:Pod/media/plex-0", namespace: "media", name: "plex-0", phase: "Running" },
    ],
    events: [],
  }) as unknown as ClusterData;

const me = (role: string) => ({ user: "u", role, scopes: ["read"], ui: { iconBaseUrl: "" } });

describe("the cluster page", () => {
  let stream: TestStream;
  afterEach(() => {
    act(() => ui.closeRestart()); // the dialog's open state is global; a leak hides the page from later tests
    stream.close();
  });

  async function show(
    role: string,
    row: PluginInfo,
    payload: ClusterData = cluster(),
    meReply: unknown = me(role),
  ) {
    const stub = stubFetch({ "GET /api/v1/me": meReply as never });
    stream = connectStream();
    renderApp(
      <>
        <ClusterPage />
        <GlobalDialogs />
      </>,
      { path: "/cluster" },
    );
    stream.plugins([row]);
    stream.send("kubernetes.cluster", payload);
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    await act(async () => {}); // /me has been read into the query cache
    await userEvent.click(screen.getByRole("tab", { name: /Workloads/ }));
    await screen.findByText("plex");
  }

  const restartButtons = () => screen.queryAllByRole("button", { name: /^Restart / });

  it("reads kubernetes.cluster: counts and the workloads tab", async () => {
    await show("admin", kubernetes());
    expect(screen.getByText("nodes").parentElement).toHaveTextContent("nodes 1/2");
    expect(screen.getByText("job-runner")).toBeInTheDocument();
  });

  it("offers Restart on a restartable workload only, and only to a role that may run it", async () => {
    await show("admin", kubernetes());
    await waitFor(() => expect(restartButtons()).toHaveLength(1));
    expect(restartButtons()[0]).toHaveAccessibleName("Restart plex");
  });

  const hidden: [string, string, PluginInfo][] = [
    ["a viewer, though the row lists the action", "viewer", kubernetes()],
    ["a row that lists no action (actions.enabled is off)", "admin", kubernetes([])],
    ["a plugin that is not visible", "admin", kubernetes([restartAction()], "unavailable")],
  ];
  for (const [what, role, row] of hidden) {
    it(`offers no Restart for ${what}`, async () => {
      await show(role, row);
      expect(restartButtons()).toHaveLength(0);
    });
  }

  it("offers Restart to a viewer when the action's minRole is viewer", async () => {
    await show("viewer", kubernetes([restartAction("viewer")]));
    await waitFor(() => expect(restartButtons()).toHaveLength(1));
  });

  it("follows a live switch of actions.enabled", async () => {
    await show("admin", kubernetes([]));
    expect(restartButtons()).toHaveLength(0);
    stream.plugins([kubernetes()]);
    await waitFor(() => expect(restartButtons()).toHaveLength(1));
    stream.plugins([kubernetes([])]);
    await waitFor(() => expect(restartButtons()).toHaveLength(0));
  });

  // The Actions column is the table's last one: header and cells appear together, or not at all.
  const actionsHeader = () => screen.queryByRole("columnheader", { name: "Actions" });
  const bodyRows = () => within(screen.getByRole("table")).getAllByRole("row").slice(1);
  const withHeadlamp = (): ClusterData => {
    const data = cluster();
    data.workloads[0] = workload("plex", {
      links: [{ label: "Headlamp", href: "https://headlamp.example.com/plex", kind: "headlamp" }],
    });
    return data;
  };

  it("draws no Actions column for a viewer with no Headlamp link", async () => {
    await show("viewer", kubernetes());
    expect(actionsHeader()).toBeNull();
    expect(screen.queryByRole("link", { name: "Open in Headlamp" })).toBeNull();
    const headers = within(screen.getByRole("table")).getAllByRole("columnheader");
    expect(headers).toHaveLength(6);
    for (const r of bodyRows()) expect(within(r).getAllByRole("cell")).toHaveLength(6);
  });

  it("draws no Actions column when actions.enabled is off and there is no Headlamp link", async () => {
    await show("admin", kubernetes([]));
    expect(actionsHeader()).toBeNull();
  });

  it("keeps the Actions column for a viewer when a row has a Headlamp link", async () => {
    await show("viewer", kubernetes(), withHeadlamp());
    expect(actionsHeader()).not.toBeNull();
    expect(screen.getAllByRole("link", { name: "Open in Headlamp" })).toHaveLength(1);
    expect(restartButtons()).toHaveLength(0);
    for (const r of bodyRows()) expect(within(r).getAllByRole("cell")).toHaveLength(7);
  });

  it("keeps the Actions column with its Restart button for an admin", async () => {
    await show("admin", kubernetes());
    expect(actionsHeader()).not.toBeNull();
    await waitFor(() => expect(restartButtons()).toHaveLength(1));
    for (const r of bodyRows()) expect(within(r).getAllByRole("cell")).toHaveLength(7);
  });

  it("keeps the Actions column while /me is still loading", async () => {
    // No Headlamp link and no available action for the role (a viewer), but the role is not known yet:
    // the column that always existed must not flicker out and back.
    await show("viewer", kubernetes(), cluster(), () => new Promise(() => {}));
    expect(actionsHeader()).not.toBeNull();
    for (const r of bodyRows()) expect(within(r).getAllByRole("cell")).toHaveLength(7);
  });

  it("opens the confirmation for the workload", async () => {
    await show("admin", kubernetes());
    await userEvent.click(await screen.findByRole("button", { name: "Restart plex" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Restart Deployment media/plex?")).toBeInTheDocument();
  });
});
