import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { PodDetail } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { FakeEventSource, pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { PodSheet } from "./pod-sheet";

// The Logs tab is the victorialogs plugin's: it follows the plugin's row. The rows are built here
// (not taken from test/shims.ts), so the test does not depend on which plugins are real yet.

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
    stubFetch({ "GET /api/pods/media/plex-0": detail });
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const tabNames = () => screen.getAllByRole("tab").map((t) => t.textContent);

  it("is there while the victorialogs plugin is visible and gone when it is disabled", async () => {
    renderApp(<PodSheet ns="media" name="plex-0" />);
    stream.plugins([logsRow()]);
    await screen.findByRole("tab", { name: "Overview" });
    expect(tabNames()).toEqual(["Overview", "Logs", "Metrics", "Events", "Links"]);

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
});
