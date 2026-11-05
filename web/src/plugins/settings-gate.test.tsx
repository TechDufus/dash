import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { me, settingsRoutes } from "@/settings/fixtures";
import { Rail } from "@/shell/rail";
import { RestartBanner } from "@/shell/restart-banner";
import { renderApp, stubFetch } from "@/test/harness";
import { shimRows } from "@/test/shims";
import { PluginRoutes } from "./routes";
import { connectStream, type TestStream } from "./test-util";

// TestViewerCannotOpenSettings (P4.8, D8): the administrator's settings are for administrators.
// A viewer has a Settings entry for the one page they may use, Tokens: /settings shows them only
// that, and no settings request is ever made for them. The anonymous viewer of the local mode and
// every user of the none mode have no tokens, so no entry and "Not authorised". An administrator
// gets the entry and the page as before (a lazy chunk, so the waits are generous).

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

/** The settings pages the page links to, in order (the rail's /settings is not one of them). */
function settingsLinks(): string[] {
  return screen
    .queryAllByRole("link")
    .map((a) => a.getAttribute("href") ?? "")
    .filter((h) => h.startsWith("/settings/") && !h.startsWith("/settings/plugins/"));
}

/** me of `role` on a dash that keeps tokens (capabilities.tokens). */
function keeping(role: "admin" | "viewer", over: Parameters<typeof me>[1] = {}) {
  const base = me(role, over);
  return { ...base, capabilities: { ...base.capabilities, tokens: true } };
}

describe("TestViewerCannotOpenSettings", () => {
  it.each([
    [
      "the anonymous viewer",
      me("viewer", { user: "anonymous", authMode: "local", scopes: ["read"] }),
    ],
    ["a viewer of the none mode", me("viewer", { authMode: "none" })],
  ])("%s sees Not authorised and makes no settings request", async (_, who) => {
    const f = stubFetch(settingsRoutes({ "GET /api/v1/me": who }));
    renderAt("/settings");
    await screen.findByText(/Not authorised/, undefined, wait);
    expect(railLabels()).not.toContain("Settings");
    expect(f.calls.filter((c) => c.path.startsWith("/api/v1/settings"))).toEqual([]);
    expect(f.calls.filter((c) => c.path.startsWith("/api/v1/tokens"))).toEqual([]);
  });

  it("a viewer's settings navigation and landing page show only Tokens", async () => {
    const f = stubFetch(
      settingsRoutes({ "GET /api/v1/me": keeping("viewer"), "GET /api/v1/tokens": { tokens: [] } }),
    );
    renderAt("/settings");
    await screen.findByRole("heading", { name: "Settings" }, wait);
    expect(railLabels()).toContain("Settings");
    expect(screen.queryByText(/Not authorised/)).toBeNull();
    expect(settingsLinks()).toEqual(["/settings/tokens"]);
    expect(screen.queryByRole("link", { name: "Audit log" })).toBeNull();
    expect(screen.queryByRole("button", { name: /Export/ })).toBeNull();

    await userEvent.click(screen.getByRole("link", { name: "Tokens" }));
    await screen.findByRole("heading", { name: "Tokens" }, wait);
    expect(await screen.findByText("No tokens yet", undefined, wait)).toBeInTheDocument();
    expect(f.calls.filter((c) => c.path.startsWith("/api/v1/settings"))).toEqual([]);
  });

  it("an administrator's navigation is unchanged, with Tokens beside the audit log", async () => {
    const f = stubFetch(settingsRoutes());
    renderAt("/settings");
    await screen.findByRole("heading", { name: "Settings" }, wait);
    expect(screen.queryByText(/Not authorised/)).toBeNull();
    expect(railLabels()).toContain("Settings");
    await waitFor(() => expect(f.callsTo("GET /api/v1/settings").length).toBeGreaterThan(0), wait);
    expect(settingsLinks()).toEqual(["/settings/audit", "/settings/tokens"]);
    expect(screen.getByRole("button", { name: /Export/ })).toBeInTheDocument();
  });

  it("drops Tokens from an administrator's settings in the none mode", async () => {
    stubFetch(settingsRoutes({ "GET /api/v1/me": me("admin", { authMode: "none" }) }));
    renderAt("/settings");
    await screen.findByRole("link", { name: "Audit log" }, wait);
    expect(settingsLinks()).toEqual(["/settings/audit"]);
  });

  // Connected apps exists only where the OAuth server is on (capabilities.oauth); every test
  // above runs without it and gets no such entry.
  it.each(["admin", "viewer"] as const)(
    "lists Connected apps for the %s once OAuth is on",
    async (role) => {
      const base = keeping(role);
      stubFetch(
        settingsRoutes({
          "GET /api/v1/me": { ...base, capabilities: { ...base.capabilities, oauth: true } },
          "GET /api/v1/oauth/grants": { grants: [] },
        }),
      );
      renderAt("/settings");
      await screen.findByRole("link", { name: "Connected apps" }, wait);
      expect(settingsLinks()).toEqual(
        role === "admin"
          ? ["/settings/audit", "/settings/tokens", "/settings/connected-apps"]
          : ["/settings/tokens", "/settings/connected-apps"],
      );
      await userEvent.click(screen.getByRole("link", { name: "Connected apps" }));
      expect(await screen.findByText("No connected apps", undefined, wait)).toBeInTheDocument();
    },
  );

  it("gives a viewer a Settings entry for Connected apps alone", async () => {
    const base = me("viewer");
    const f = stubFetch(
      settingsRoutes({
        "GET /api/v1/me": { ...base, capabilities: { ...base.capabilities, oauth: true } },
        "GET /api/v1/oauth/grants": { grants: [] },
      }),
    );
    renderAt("/settings");
    await screen.findByRole("heading", { name: "Settings" }, wait);
    expect(railLabels()).toContain("Settings");
    expect(settingsLinks()).toEqual(["/settings/connected-apps"]);
    expect(f.calls.filter((c) => c.path.startsWith("/api/v1/tokens"))).toEqual([]);
  });

  // Without a store that keeps tokens a viewer has no settings page of their own: no Settings
  // entry, and nothing asks for tokens.
  it("gives a viewer no Settings entry where tokens are not kept", async () => {
    const f = stubFetch(settingsRoutes({ "GET /api/v1/me": me("viewer") }));
    renderAt("/");
    await screen.findByRole("navigation", { name: "Primary" }, wait);
    expect(railLabels()).not.toContain("Settings");
    expect(f.calls.filter((c) => c.path.startsWith("/api/v1/tokens"))).toEqual([]);
  });

  it("opens Tokens for a viewer", async () => {
    stubFetch(
      settingsRoutes({ "GET /api/v1/me": keeping("viewer"), "GET /api/v1/tokens": { tokens: [] } }),
    );
    renderAt("/settings/tokens");
    await screen.findByRole("heading", { name: "Tokens" }, wait);
    expect(screen.queryByText(/Not authorised/)).toBeNull();
  });

  it("gates the plugin settings page the same way", async () => {
    const f = stubFetch(settingsRoutes({ "GET /api/v1/me": me("viewer") }));
    renderAt("/settings/plugins/proxmox");
    await screen.findByText(/Not authorised/, undefined, wait);
    expect(f.calls.filter((c) => c.path.startsWith("/api/v1/settings"))).toEqual([]);
  });

  it("opens a plugin's form for an administrator", async () => {
    const f = stubFetch(settingsRoutes());
    renderAt("/settings/plugins/proxmox");
    await screen.findByRole("heading", { name: "Proxmox VE" }, wait);
    await screen.findByLabelText(/node/i, undefined, wait);
    await waitFor(
      () => expect(f.callsTo("GET /api/v1/settings/plugins/proxmox").length).toBeGreaterThan(0),
      wait,
    );
  });

  it("gates the audit page the same way", async () => {
    const viewer = stubFetch(settingsRoutes({ "GET /api/v1/me": me("viewer") }));
    renderAt("/settings/audit");
    await screen.findByText(/Not authorised/, undefined, wait);
    expect(viewer.calls.filter((c) => c.path.startsWith("/api/v1/settings"))).toEqual([]);
  });

  it("opens the audit page from the Settings page", async () => {
    const f = stubFetch(settingsRoutes());
    renderAt("/settings");
    await userEvent.click(await screen.findByRole("link", { name: "Audit log" }, wait));
    await screen.findByText(/since this start/, undefined, wait);
    await waitFor(
      () => expect(f.callsTo("GET /api/v1/settings/audit").length).toBeGreaterThan(0),
      wait,
    );
  });

  // Demo mode serves no settings API (every settings route is 404): its administrator gets the
  // Tokens landing, the other settings pages say so, and nothing (the restart notice included)
  // ever requests /api/v1/settings.
  it.each([
    ["/settings", "heading"],
    ["/settings/audit", "notice"],
    ["/settings/plugins/proxmox", "notice"],
  ])("in demo mode %s makes no settings request", async (path, shows) => {
    const f = stubFetch(
      settingsRoutes({
        "GET /api/v1/me": keeping("admin", { authMode: "demo" }),
        "GET /api/v1/tokens": { tokens: [] },
      }),
    );
    renderApp(
      <>
        <RestartBanner />
        <Rail />
        <PluginRoutes />
      </>,
      { path },
    );
    stream.plugins(shimRows());
    if (shows === "heading") {
      await screen.findByRole("heading", { name: "Settings" }, wait);
      expect(settingsLinks()).toEqual(["/settings/tokens"]);
      expect(railLabels()).toContain("Settings");
    } else {
      await screen.findByText(/not available in demo mode/, undefined, wait);
    }
    expect(f.calls.filter((c) => c.path.startsWith("/api/v1/settings"))).toEqual([]);
  });
});
