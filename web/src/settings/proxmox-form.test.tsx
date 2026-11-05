// P4.9 acceptance: a Proxmox config is entered, tested and saved against a fake backend, and the
// plugin's state turns healthy from core.plugins without a reload.
import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import type { SettingField, SettingsPluginView } from "@/api/types";
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
  checkOK,
  field,
  patchResult,
  proxmoxFields,
  proxmoxView,
  REVISION,
  settingsRoutes,
} from "./fixtures";
import { PluginSettingsPage } from "./plugin-settings-page";

const P = "plugins.proxmox.";
const URL_ = "https://pve.lab.test:8006";
const SECRET = "pve-token-secret-0f3a";

/** Proxmox before it is configured: no URL, node, token ID or token secret. */
function unconfigured(): SettingsPluginView {
  const blank: Record<string, SettingField> = {
    [`${P}url`]: field(`${P}url`, "string", { value: "" }),
    [`${P}node`]: field(`${P}node`, "string", { value: "" }),
    [`${P}tokenID`]: field(`${P}tokenID`, "string", { value: "" }),
    [`${P}tokenSecret`]: field(`${P}tokenSecret`, "string", { secret: true }),
  };
  return proxmoxView({ fields: proxmoxFields().map((f) => blank[f.path] ?? f) });
}

/** The view after the save: the three leaves and the secret stored from the UI. */
function saved(prev: SettingsPluginView, patch: Record<string, unknown>): SettingsPluginView {
  return {
    ...prev,
    revision: REVISION + 1,
    fields: prev.fields.map((f) => {
      const rel = f.path.slice(P.length);
      if (!(rel in patch)) return f;
      return f.secret
        ? { ...f, set: true, source: "ui" as const, stored: true }
        : { ...f, value: patch[rel], set: true, source: "ui" as const, stored: true };
    }),
  };
}

let ES: typeof FakeEventSource;

beforeEach(() => {
  ES = installEventSource();
});

afterEach(() => {
  stopStream();
});

describe("proxmox-form", () => {
  it("enters, tests and saves a Proxmox config; the row turns healthy from core.plugins", async () => {
    let view = unconfigured();
    const stub = stubFetch(
      settingsRoutes({
        [`GET ${settingsPaths.plugin("proxmox")}`]: () => view,
        [`PUT ${settingsPaths.plugin("proxmox")}`]: ({
          body,
        }: {
          body: Record<string, unknown>;
        }) => {
          view = saved(view, body);
          return patchResult();
        },
        [`POST ${settingsPaths.test("proxmox")}`]: checkOK(),
      }),
    );
    startStream();
    const es = ES.instances.at(-1) as FakeEventSource;
    es.open();
    const row = pluginInfo({ id: "proxmox", name: "Proxmox", configurable: true });
    es.emit("core.plugins", pluginsEnvelope([{ ...row, state: "unconfigured" }]));
    const user = userEvent.setup();
    renderApp(<PluginSettingsPage id="proxmox" />);

    await user.type(await screen.findByLabelText("url"), URL_);
    await user.type(screen.getByLabelText("node"), "pve");
    await user.type(screen.getByLabelText("tokenID"), "dash@pve!dash");
    await user.click(screen.getByRole("button", { name: "Replace tokenSecret" }));
    await user.type(screen.getByLabelText("New tokenSecret"), SECRET);

    await user.click(screen.getByRole("button", { name: "Test connection" }));
    const result = await screen.findByRole("region", { name: "Connection test" });
    expect(within(result).getByText(/^version: ok/)).toBeInTheDocument();
    expect(stub.callsTo(`POST ${settingsPaths.test("proxmox")}`).map((c) => c.body)).toEqual([
      {
        patch: { url: URL_, node: "pve", tokenID: "dash@pve!dash" },
        secrets: { tokenSecret: SECRET },
      },
    ]);
    expect(stub.callsTo(`PUT ${settingsPaths.plugin("proxmox")}`)).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Saved."));
    // A new URL moves the destination (D16): the secret rides in the same If-Match'd patch.
    const puts = stub.callsTo(`PUT ${settingsPaths.plugin("proxmox")}`);
    expect(puts.map((c) => [c.headers.get("If-Match"), c.body])).toEqual([
      [`"${REVISION}"`, { url: URL_, node: "pve", tokenID: "dash@pve!dash", tokenSecret: SECRET }],
    ]);
    expect(screen.getByLabelText("url")).toHaveValue(URL_);
    const secret = screen.getByRole("group", { name: "tokenSecret" });
    expect(within(secret).getByText("Set (ui)")).toBeInTheDocument();

    const state = () => screen.getByText("State").nextElementSibling;
    expect(state()).toHaveTextContent("unconfigured");
    act(() => es.emit("core.plugins", pluginsEnvelope([{ ...row, state: "healthy" }])));
    expect(state()).toHaveTextContent("healthy");
    expect(ES.instances).toHaveLength(1);
  });

  // Pins that the form's enabled switch shows a running plugin whose `enabled` is unset as ON:
  // from the server's value, and from the live core.plugins state when the value is absent.
  it.each([
    ["the server's value", { value: true }],
    ["the live state when the value is absent", {}],
  ])("shows enabled ON for a running plugin from %s", async (_, over) => {
    const fields = proxmoxFields().map((f) =>
      f.path === `${P}enabled`
        ? field(`${P}enabled`, "boolean", { set: false, source: "default", ...over })
        : f,
    );
    stubFetch(
      settingsRoutes({ [`GET ${settingsPaths.plugin("proxmox")}`]: proxmoxView({ fields }) }),
    );
    startStream();
    const es = ES.instances.at(-1) as FakeEventSource;
    es.open();
    es.emit(
      "core.plugins",
      pluginsEnvelope([pluginInfo({ id: "proxmox", name: "Proxmox VE", state: "healthy" })]),
    );
    renderApp(<PluginSettingsPage id="proxmox" />);
    expect(await screen.findByRole("switch", { name: "enabled" })).toBeChecked();
  });

  // Pins the page frame: the plugin's name is the page's h1, like Settings' own.
  it("titles the page with the plugin's name as its h1", async () => {
    stubFetch(settingsRoutes());
    startStream();
    const es = ES.instances.at(-1) as FakeEventSource;
    es.open();
    es.emit("core.plugins", pluginsEnvelope([pluginInfo({ id: "proxmox", name: "Proxmox VE" })]));
    renderApp(<PluginSettingsPage id="proxmox" />);
    expect(
      await screen.findByRole("heading", { level: 1, name: "Proxmox VE" }),
    ).toBeInTheDocument();
  });
});
