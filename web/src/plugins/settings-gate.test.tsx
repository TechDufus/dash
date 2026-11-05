import { screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { me, settingsRoutes } from "@/settings/fixtures";
import { Rail } from "@/shell/rail";
import { renderApp, stubFetch } from "@/test/harness";
import { shimRows } from "@/test/shims";
import { PluginRoutes } from "./routes";
import { connectStream, type TestStream } from "./test-util";

// TestViewerCannotOpenSettings (P4.8): the settings are for administrators. A viewer, the
// anonymous viewer of the local mode included, has no Settings entry in the navigation and gets
// "Not authorised" at /settings, and no settings request is ever made for them. An administrator
// gets the entry and the page (a lazy chunk, so the waits are generous).

let stream: TestStream;
beforeEach(() => {
  stream = connectStream();
});
afterEach(() => stream.close());

function renderAt(path: string) {
  renderApp(
    <>
      <Rail />
      <PluginRoutes />
    </>,
    { path },
  );
  stream.plugins(shimRows());
}

function railLabels(): string[] {
  const rail = screen.getByRole("navigation", { name: "Primary" });
  return within(rail)
    .getAllByRole("link")
    .map((a) => a.getAttribute("aria-label") ?? "");
}

const wait = { timeout: 5000 };

describe("TestViewerCannotOpenSettings", () => {
  it.each([
    ["a viewer", me("viewer")],
    [
      "the anonymous viewer",
      me("viewer", { user: "anonymous", authMode: "local", scopes: ["read"] }),
    ],
  ])("%s sees Not authorised and makes no settings request", async (_, who) => {
    const f = stubFetch(settingsRoutes({ "GET /api/v1/me": who }));
    renderAt("/settings");
    await screen.findByText(/Not authorised/, undefined, wait);
    expect(railLabels()).not.toContain("Settings");
    expect(f.calls.filter((c) => c.path.startsWith("/api/v1/settings"))).toEqual([]);
  });

  it("an administrator gets the entry and the page", async () => {
    const f = stubFetch(settingsRoutes());
    renderAt("/settings");
    await screen.findByRole("heading", { name: "Settings" }, wait);
    expect(screen.queryByText(/Not authorised/)).toBeNull();
    expect(railLabels()).toContain("Settings");
    expect(f.callsTo("GET /api/v1/settings").length).toBeGreaterThan(0);
  });
});
