import { act, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import { ui } from "@/api/ui-store";
import { Shell } from "@/shell/shell";
import {
  envelope,
  type FakeEventSource,
  installEventSource,
  pluginsEnvelope,
  renderApp,
  stubFetch,
} from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
import { PluginRoutes } from "./routes";

let ES: typeof FakeEventSource;

beforeEach(() => {
  ES = installEventSource();
  stubFetch({
    "GET /api/v1/me": { user: "dev", role: "admin", version: "test" },
    "GET /api/v1/plugins": () => new Promise(() => {}),
  });
});

afterEach(() => {
  act(() => ui.setPalette(false));
  stopStream();
});

// The frontend half of the G2 acceptance: a plugin shows up in the nav and disappears when it is
// disabled, in the rail, the route and the palette, all without a reload.
describe("a plugin that is switched off while the page is open", () => {
  it("disappears from the nav, the route and the palette, and comes back", async () => {
    startStream();
    const es = ES.instances[0];
    if (!es) throw new Error("no EventSource");
    es.open();
    es.emit("core.plugins", pluginsEnvelope(shimRows()));
    es.emit("status", envelope("status", { endpoints: [] }));

    renderApp(
      <Shell>
        <PluginRoutes />
      </Shell>,
      { path: "/status" },
    );
    const rail = screen.getByRole("navigation", { name: "Primary" });
    expect(within(rail).getByRole("link", { name: "Status" })).toBeInTheDocument();
    expect(await screen.findByText("No endpoints reported.")).toBeInTheDocument();

    act(() => ui.setPalette(true));
    const palette = await screen.findByRole("dialog", { name: "Command palette" });
    expect(within(palette).getByText("Status")).toBeInTheDocument();

    // The server disables the plugin: one core.plugins event on the open stream.
    act(() => es.emit("core.plugins", pluginsEnvelope(withState(shimRows(), "gatus", "disabled"))));

    expect(within(rail).queryByRole("link", { name: "Status", hidden: true })).toBeNull();
    expect(screen.queryByText("No endpoints reported.")).toBeNull();
    expect(screen.getByText(/Gatus is switched off\./)).toBeInTheDocument();
    expect(within(palette).queryByText("Status")).toBeNull();
    expect(ES.instances).toHaveLength(1);

    // And back on.
    act(() => es.emit("core.plugins", pluginsEnvelope(shimRows())));
    act(() => es.emit("status", envelope("status", { endpoints: [] })));
    expect(within(rail).getByRole("link", { name: "Status", hidden: true })).toBeInTheDocument();
    expect(await screen.findByText("No endpoints reported.")).toBeInTheDocument();
    expect(within(palette).getByText("Status")).toBeInTheDocument();
  });
});
