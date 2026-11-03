import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ActionInfo, PluginInfo, PodDetail } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { FakeEventSource, pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { PodSheet } from "./pod-sheet";

// The Logs tab is the victorialogs plugin's: it follows the plugin's row, and the principal's
// logs scope. The rows are built here (not taken from test/shims.ts), so the test does not depend
// on which plugins are real yet.

const meWith = (scopes: string[]) => ({
  user: "u",
  role: "viewer",
  scopes,
  ui: { iconBaseUrl: "" },
});

const detail = {
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
  containers: [
    {
      name: "plex",
      image: "plex:1",
      state: "running",
      ready: true,
      restarts: 0,
      lastTerminationReason: "",
    },
  ],
  ownerChain: [],
  events: [],
} as unknown as PodDetail;

const logsRow = (state: string = "healthy") =>
  pluginInfo({ id: "victorialogs", name: "VictoriaLogs", state: state as never });

describe("the pod sheet's Logs tab", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch({
      "GET /api/v1/plugins/kubernetes/pods/media/plex-0": detail,
      "GET /api/v1/me": meWith(["read", "logs"]),
    });
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const tabNames = () => screen.getAllByRole("tab").map((t) => t.textContent);

  it("is there while the victorialogs plugin is visible and gone when it is disabled", async () => {
    renderApp(<PodSheet ns="media" name="plex-0" />);
    stream.plugins([logsRow()]);
    await screen.findByRole("tab", { name: "Overview" });
    await waitFor(() =>
      expect(tabNames()).toEqual(["Overview", "Logs", "Metrics", "Events", "Links"]),
    );

    stream.plugins([logsRow("disabled")]);
    await waitFor(() => expect(tabNames()).toEqual(["Overview", "Metrics", "Events", "Links"]));

    stream.plugins([logsRow()]);
    await waitFor(() => expect(tabNames()).toContain("Logs"));
  });

  it("is not offered when no plugin row exists for it, or it is not configured", async () => {
    renderApp(<PodSheet ns="media" name="plex-0" />);
    stream.plugins([pluginInfo({ id: "kubernetes", name: "Kubernetes" })]);
    await screen.findByRole("tab", { name: "Overview" });
    expect(screen.queryByRole("tab", { name: "Logs" })).toBeNull();

    stream.plugins([logsRow("unconfigured")]);
    await waitFor(() => expect(screen.queryByRole("tab", { name: "Logs" })).toBeNull());
    stream.plugins([logsRow("unavailable")]);
    await waitFor(() => expect(screen.queryByRole("tab", { name: "Logs" })).toBeNull());
  });

  it("falls back to Overview when the plugin goes away under an open Logs tab", async () => {
    const user = userEvent.setup();
    renderApp(<PodSheet ns="media" name="plex-0" />);
    stream.plugins([logsRow()]);
    await user.click(await screen.findByRole("tab", { name: "Logs" }));
    expect(screen.getByRole("tab", { name: "Logs" })).toHaveAttribute("aria-selected", "true");

    stream.plugins([logsRow("disabled")]);
    await waitFor(() => expect(screen.queryByRole("tab", { name: "Logs" })).toBeNull());
    expect(screen.getByRole("tab", { name: "Overview" })).toHaveAttribute("aria-selected", "true");
  });

  it("tails through the plugin's route", async () => {
    const user = userEvent.setup();
    renderApp(<PodSheet ns="media" name="plex-0" />);
    stream.plugins([logsRow()]);
    await user.click(await screen.findByRole("tab", { name: "Logs" }));
    await user.click(await screen.findByRole("button", { name: "Live" }));

    const tail = FakeEventSource.instances.find((es) => es.url.includes("/logs/tail"));
    expect(tail?.url).toBe("/api/v1/plugins/victorialogs/logs/tail?ns=media&pod=plex-0");
  });

  it("is not offered to a principal without the logs scope (the anonymous viewer)", async () => {
    const me = stubFetch({
      "GET /api/v1/plugins/kubernetes/pods/media/plex-0": detail,
      "GET /api/v1/me": { ...meWith(["read"]), user: "anonymous" },
    });
    renderApp(<PodSheet ns="media" name="plex-0" />);
    stream.plugins([logsRow()]);
    await screen.findByRole("tab", { name: "Overview" });
    await waitFor(() => expect(me.callsTo("GET /api/v1/me")).toHaveLength(1));
    await act(async () => {}); // /me has been read into the query cache
    expect(tabNames()).toEqual(["Overview", "Metrics", "Events", "Links"]);
  });
});

describe("the pod sheet's Restart control", () => {
  let stream: TestStream;
  afterEach(() => stream.close());

  const restartAction = (minRole = "admin"): ActionInfo => ({
    name: "restart",
    title: "Restart",
    destructive: true,
    idempotent: false,
    confirm: "none",
    minRole,
    inputSchema: null,
  });
  const kubernetes = (actions: ActionInfo[], state = "healthy") =>
    pluginInfo({
      id: "kubernetes",
      name: "Kubernetes",
      state: state as never,
      topics: ["kubernetes.cluster"],
      actions,
    });
  const owned = { ...detail, ownerChain: [{ kind: "Deployment", name: "plex" }] } as PodDetail;
  const button = () => screen.queryByRole("button", { name: "Restart Deployment plex" });

  async function show(role: string, row: PluginInfo) {
    const me = stubFetch({
      "GET /api/v1/plugins/kubernetes/pods/media/plex-0": owned,
      "GET /api/v1/me": { user: "u", role, scopes: ["read"], ui: { iconBaseUrl: "" } },
    });
    stream = connectStream();
    renderApp(<PodSheet ns="media" name="plex-0" />);
    stream.plugins([row]);
    await screen.findByText("Owner");
    await waitFor(() => expect(me.callsTo("GET /api/v1/me")).toHaveLength(1));
    await act(async () => {}); // /me has been read into the query cache
  }

  it("is offered to an admin while the row lists the action", async () => {
    await show("admin", kubernetes([restartAction()]));
    await waitFor(() => expect(button()).not.toBeNull());
  });

  it("is not offered to a viewer, though the row lists the action", async () => {
    await show("viewer", kubernetes([restartAction()]));
    expect(button()).toBeNull();
  });

  it("is offered to a viewer when the action's minRole is viewer", async () => {
    await show("viewer", kubernetes([restartAction("viewer")]));
    await waitFor(() => expect(button()).not.toBeNull());
  });

  it("is not offered while the row lists no action (actions.enabled is off)", async () => {
    await show("admin", kubernetes([]));
    expect(button()).toBeNull();
  });

  it("is not offered while the plugin is not visible", async () => {
    await show("admin", kubernetes([restartAction()], "unavailable"));
    expect(button()).toBeNull();
  });

  it("follows a live switch of actions.enabled", async () => {
    await show("admin", kubernetes([]));
    expect(button()).toBeNull();
    stream.plugins([kubernetes([restartAction()])]);
    await waitFor(() => expect(button()).not.toBeNull());
    stream.plugins([kubernetes([])]);
    await waitFor(() => expect(button()).toBeNull());
  });
});
