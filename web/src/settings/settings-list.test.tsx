import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import {
  type FakeEventSource,
  installEventSource,
  pluginInfo,
  pluginsEnvelope,
  renderApp,
  stubFetch,
} from "@/test/harness";
import { settingsPaths } from "./api";
import {
  field,
  patchResult,
  REVISION,
  readonlySettingsView,
  settingsPlugin,
  settingsRoutes,
  settingsView,
  staleRevision,
} from "./fixtures";
import { SettingsPage } from "./settings-page";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

let ES: typeof FakeEventSource;

beforeEach(() => {
  ES = installEventSource();
  vi.mocked(toast.error).mockClear();
});

afterEach(() => {
  stopStream();
});

function connect(): FakeEventSource {
  startStream();
  const es = ES.instances.at(-1);
  if (!es) throw new Error("no EventSource");
  es.open();
  return es;
}

const LIVE = [
  pluginInfo({ id: "proxmox", name: "Proxmox VE", state: "healthy" }),
  pluginInfo({ id: "grafana", name: "Grafana", state: "disabled", message: "not configured" }),
  pluginInfo({ id: "hardware", name: "Hardware", state: "starting" }),
  pluginInfo({ id: "launcher", name: "Launcher", state: "healthy" }),
];

/** The list row of the switch named "<name> enabled". */
function row(name: string): HTMLElement {
  const li = screen.getByRole("switch", { name: `${name} enabled` }).closest("li");
  if (!li) throw new Error(`no row ${name}`);
  return li;
}

describe("SettingsPage plugin list", () => {
  it("updates a row's state and message live from core.plugins, with no settings reload", async () => {
    const f = stubFetch(settingsRoutes());
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    await screen.findByRole("switch", { name: "Proxmox VE enabled" });
    expect(within(row("Proxmox VE")).getByText("healthy")).toBeInTheDocument();

    act(() =>
      es.emit(
        "core.plugins",
        pluginsEnvelope([
          { ...LIVE[0], state: "error", message: "connection refused" },
          ...LIVE.slice(1),
        ]),
      ),
    );
    expect(within(row("Proxmox VE")).getByText("error")).toBeInTheDocument();
    expect(within(row("Proxmox VE")).getByText("connection refused")).toBeInTheDocument();
    expect(f.callsTo(`GET ${settingsPaths.view}`)).toHaveLength(1);
  });

  // Pins that an unset `enabled` follows the live state, not the value of the last settings read:
  // a background tab's read can say false while core.plugins already reports the instance running.
  it("shows an unset enabled ON when its read value is false but the live state runs", async () => {
    const stale = settingsPlugin("proxmox", {
      name: "Proxmox VE",
      enabled: field("plugins.proxmox.enabled", "boolean", { value: false }),
    });
    stubFetch(
      settingsRoutes({ [`GET ${settingsPaths.view}`]: settingsView({ plugins: [stale] }) }),
    );
    const es = connect();
    es.emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    expect(await screen.findByRole("switch", { name: "Proxmox VE enabled" })).toBeChecked();
  });

  it("toggles a plugin with the last read revision, then reads the settings again", async () => {
    const f = stubFetch(
      settingsRoutes({ [`PUT ${settingsPaths.plugin("hardware")}`]: patchResult() }),
    );
    connect().emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    const u = userEvent.setup();
    const sw = await screen.findByRole("switch", { name: "Hardware enabled" });
    expect(sw).toBeChecked(); // unset and starting: it runs
    await u.click(sw);

    await waitFor(() => expect(f.callsTo(`GET ${settingsPaths.view}`)).toHaveLength(2));
    const [put] = f.callsTo(`PUT ${settingsPaths.plugin("hardware")}`);
    expect(put.body).toEqual({ enabled: false });
    expect(put.headers.get("If-Match")).toBe(`"${REVISION}"`);
  });

  it("reports a refused toggle and keeps the row usable", async () => {
    const f = stubFetch(
      settingsRoutes({ [`PUT ${settingsPaths.plugin("proxmox")}`]: () => staleRevision() }),
    );
    connect().emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    const u = userEvent.setup();
    const sw = await screen.findByRole("switch", { name: "Proxmox VE enabled" });
    await u.click(sw);
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(f.callsTo(`PUT ${settingsPaths.plugin("proxmox")}`)[0].body).toEqual({ enabled: false });
    expect(sw).toBeEnabled();
  });

  // Pins that a failed toggle still refetches the settings: after a 412 the next attempt carries
  // the revision of the fresh read, not the stale one that was just refused.
  it("retries a toggle refused with 412 with the revision of the settings read again", async () => {
    const put = `PUT ${settingsPaths.plugin("proxmox")}`;
    const f = stubFetch(
      settingsRoutes({
        [put]: () => {
          f.route(`GET ${settingsPaths.view}`, settingsView({ revision: REVISION + 1 }));
          f.route(put, patchResult());
          return staleRevision();
        },
      }),
    );
    connect().emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    const u = userEvent.setup();
    await u.click(await screen.findByRole("switch", { name: "Proxmox VE enabled" }));
    await waitFor(() => expect(f.callsTo(`GET ${settingsPaths.view}`)).toHaveLength(2));
    const sw = await screen.findByRole("switch", { name: "Proxmox VE enabled" });
    await waitFor(() => expect(sw).toBeEnabled());
    await u.click(sw);
    await waitFor(() => expect(f.callsTo(put)).toHaveLength(2));
    expect(f.callsTo(put).map((c) => c.headers.get("If-Match"))).toEqual([
      `"${REVISION}"`,
      `"${REVISION + 1}"`,
    ]);
  });

  // Pins that an unset `enabled` shows what runs: the server's value (whether the instance runs),
  // and the live state when it sends none, never OFF for a running plugin.
  it.each([
    ["the server's value", { value: true }],
    ["the live state when the value is absent", {}],
  ])("shows a running plugin with unset enabled as ON from %s", async (_, over) => {
    const put = `PUT ${settingsPaths.plugin("proxmox")}`;
    const enabled = field("plugins.proxmox.enabled", "boolean", {
      set: false,
      source: "default",
      ...over,
    });
    const v = settingsView({
      plugins: [settingsPlugin("proxmox", { name: "Proxmox VE", enabled })],
    });
    const f = stubFetch(settingsRoutes({ [`GET ${settingsPaths.view}`]: v, [put]: patchResult() }));
    connect().emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    const u = userEvent.setup();
    const sw = await screen.findByRole("switch", { name: "Proxmox VE enabled" });
    expect(sw).toBeChecked();
    await u.click(sw);
    await waitFor(() => expect(f.callsTo(put)).toHaveLength(1));
    expect(f.callsTo(put)[0]?.body).toEqual({ enabled: false });
  });

  // Pins that a message equal to the state is not shown twice (the badge already says it).
  it("hides a row message that repeats the state", async () => {
    stubFetch(settingsRoutes());
    connect().emit(
      "core.plugins",
      pluginsEnvelope([{ ...LIVE[1], state: "disabled", message: "disabled" }]),
    );
    renderApp(<SettingsPage />);
    await screen.findByRole("switch", { name: "Grafana enabled" });
    expect(within(row("Grafana")).getAllByText("disabled")).toHaveLength(1);
  });

  it("disables a locked toggle and names its source on a focusable lock", async () => {
    const f = stubFetch(settingsRoutes());
    connect().emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    const u = userEvent.setup();
    const sw = await screen.findByRole("switch", { name: "Grafana enabled" });
    expect(sw).toBeDisabled();
    expect(sw).toHaveAccessibleDescription("Set by env DASH_PLUGINS_GRAFANA_ENABLED");

    const lock = screen.getByRole("button", {
      name: "Locked: Set by env DASH_PLUGINS_GRAFANA_ENABLED",
    });
    lock.focus();
    expect(await screen.findByRole("tooltip")).toHaveTextContent(
      "Set by env DASH_PLUGINS_GRAFANA_ENABLED",
    );
    await u.click(sw);
    expect(f.calls.filter((c) => c.method === "PUT")).toEqual([]);
  });

  it("shows the read-only banner from store.reason and locks every toggle", async () => {
    stubFetch(settingsRoutes({ [`GET ${settingsPaths.view}`]: readonlySettingsView() }));
    connect().emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    const banner = await screen.findByRole("status");
    expect(banner).toHaveTextContent("Settings are read-only");
    expect(banner).toHaveTextContent("settings.ui is readonly");
    for (const sw of screen.getAllByRole("switch")) expect(sw).toBeDisabled();
    expect(screen.queryByRole("textbox", { name: "ui.iconBaseURL" })).toBeNull();
  });

  it("locks every toggle of a read-only store even when a field is not marked locked", async () => {
    const v = settingsView({
      store: { readonly: true, reason: "data_dir", dataDir: "", writable: false, secretKey: false },
      plugins: [
        settingsPlugin("hardware", {
          name: "Hardware",
          enabled: field("plugins.hardware.enabled", "boolean"),
        }),
      ],
    });
    stubFetch(settingsRoutes({ [`GET ${settingsPaths.view}`]: v }));
    connect().emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    expect(await screen.findByRole("switch", { name: "Hardware enabled" })).toBeDisabled();
  });

  it("is keyboard operable: labelled controls in reading order, Space toggles", async () => {
    const f = stubFetch(
      settingsRoutes({ [`PUT ${settingsPaths.plugin("hardware")}`]: patchResult() }),
    );
    connect().emit("core.plugins", pluginsEnvelope(LIVE));
    renderApp(<SettingsPage />);
    await screen.findByRole("switch", { name: "Hardware enabled" });
    const u = userEvent.setup();

    const order: string[] = [];
    for (let i = 0; i < 12; i++) {
      await u.tab();
      const el = document.activeElement as HTMLElement;
      order.push(`${el.getAttribute("role") ?? el.tagName.toLowerCase()}:${accessibleName(el)}`);
    }
    // The locked Grafana toggle is disabled, so Tab skips it and stops on its lock, which names
    // the reason; Launcher is not configurable.
    expect(order).toEqual([
      "a:Audit log",
      "a:MCP",
      "a:Tokens",
      "button:Export as config.yaml snippet",
      "a:Configure Proxmox VE",
      "switch:Proxmox VE enabled",
      "a:Configure Grafana",
      "button:Locked: Set by env DASH_PLUGINS_GRAFANA_ENABLED",
      "a:Configure Hardware",
      "switch:Hardware enabled",
      "switch:Launcher enabled",
      "input:ui.iconBaseURL",
    ]);

    // Hardware's enabled is unset and it is starting: it runs, so Space turns it off.
    screen.getByRole("switch", { name: "Hardware enabled" }).focus();
    await u.keyboard(" ");
    await waitFor(() =>
      expect(f.callsTo(`PUT ${settingsPaths.plugin("hardware")}`)[0]?.body).toEqual({
        enabled: false,
      }),
    );
  });
});

/** The accessible name the tests need: aria-label, the associated label, or the text. */
function accessibleName(el: HTMLElement): string {
  const labels = (el as HTMLInputElement).labels;
  if (labels && labels.length > 0) return labels[0].textContent?.replace(/\s+/g, " ").trim() ?? "";
  return (el.getAttribute("aria-label") ?? el.textContent ?? "").replace(/\s+/g, " ").trim();
}
