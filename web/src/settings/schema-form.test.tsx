import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { SettingsPluginView } from "@/api/types";
import { renderApp, stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import {
  envField,
  field,
  hardwareView,
  invalidFields,
  proxmoxFields,
  proxmoxView,
  REVISION,
  secretLocked,
  settingsRoutes,
} from "./fixtures";
import { REENTER } from "./form-model";
import { PluginSettingsPage } from "./plugin-settings-page";

const P = "plugins.proxmox.";
const PLUGIN_PUT = `PUT ${settingsPaths.plugin("proxmox")}`;
const SECRET_PUT = `PUT ${settingsPaths.secret("proxmox", "tokenSecret")}`;

/** proxmoxView with some fields replaced by path. */
function proxmoxWith(...over: SettingsPluginView["fields"]): SettingsPluginView {
  const by = new Map(over.map((f) => [f.path, f]));
  return proxmoxView({ fields: proxmoxFields().map((f) => by.get(f.path) ?? f) });
}

function open(routes: Record<string, unknown> = {}, id = "proxmox") {
  const stub = stubFetch(settingsRoutes(routes));
  const user = userEvent.setup();
  renderApp(<PluginSettingsPage id={id} />);
  return { stub, user };
}

const saved = () => waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Saved."));

describe("TestFormIsKeyboardOperable", () => {
  it("associates a label with every control", async () => {
    open();
    expect(await screen.findByLabelText("url")).toHaveValue("https://pve.example.com:8006");
    expect(screen.getByLabelText("node")).toHaveValue("pve");
    expect(screen.getByLabelText("caFile")).toHaveValue("");
    expect(screen.getByRole("switch", { name: "insecureSkipVerify" })).not.toBeChecked();
    expect(screen.getByRole("textbox", { name: "ids" })).toHaveValue("");
    expect(screen.getByRole("combobox", { name: "source" })).toHaveValue("api");
    expect(screen.getByRole("group", { name: "tokenSecret" })).toHaveTextContent("Set (ui)");
  });

  it("moves through the fields in the form's order with Tab", async () => {
    const { user } = open();
    (await screen.findByLabelText("url")).focus();
    const order = [
      screen.getByRole("button", { name: "Reset url to default" }),
      screen.getByLabelText("node"),
      screen.getByRole("button", { name: "Reset node to default" }),
      screen.getByRole("button", { name: "More about node" }),
      screen.getByLabelText("tokenID"),
      screen.getByRole("button", { name: "More about tokenID" }),
      screen.getByRole("button", { name: "Replace tokenSecret" }),
      screen.getByRole("button", { name: "Clear tokenSecret" }),
      screen.getByRole("button", { name: "More about tokenSecret" }),
      screen.getByLabelText("caFile"),
      screen.getByRole("switch", { name: "insecureSkipVerify" }),
    ];
    for (const el of order) {
      await user.tab();
      expect(el).toHaveFocus();
    }
  });

  it("submits with Enter and sends If-Match of the view's revision", async () => {
    const { stub, user } = open();
    await user.type(await screen.findByLabelText("node"), "2{Enter}");
    await saved();
    expect(stub.callsTo(PLUGIN_PUT).map((c) => [c.headers.get("If-Match"), c.body])).toEqual([
      [`"${REVISION}"`, { node: "pve2" }],
    ]);
  });

  it("closes the replace field with Escape and forgets the value", async () => {
    const { stub, user } = open();
    (await screen.findByRole("button", { name: "More about tokenID" })).focus();
    await user.tab();
    await user.keyboard("{Enter}");
    const input = screen.getByLabelText("New tokenSecret");
    expect(input).toHaveFocus();
    await user.type(input, "typed{Escape}");
    expect(screen.queryByLabelText("New tokenSecret")).toBeNull();
    expect(screen.getByRole("button", { name: "Replace tokenSecret" })).toHaveFocus();

    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByRole("status")).toHaveTextContent("Nothing to save.");
    expect(stub.callsTo(SECRET_PUT)).toHaveLength(0);
  });

  it("reports errors in a role=alert and under each field it names", async () => {
    const message = "must hold {vmid} once";
    const { user } = open({
      [PLUGIN_PUT]: invalidFields([
        { path: `${P}series.vmSelector`, message },
        { path: `${P}nowhere`, message: "unknown setting" },
      ]),
    });
    const selector = await screen.findByLabelText("vmSelector");
    await user.clear(selector);
    await user.type(selector, "id=x{Enter}");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(`${P}nowhere: unknown setting`);
    expect(selector).toHaveAttribute("aria-invalid", "true");
    expect(selector).toHaveAccessibleDescription(expect.stringContaining(message));
    expect(screen.getByLabelText("node")).not.toHaveAttribute("aria-invalid");
  });
});

describe("schema-driven form", () => {
  it("renders $defs objects as fieldsets and enums as selects", async () => {
    open();
    const tls = await screen.findByRole("group", { name: "tls" });
    expect(within(tls).getByLabelText("caFile")).toBeInTheDocument();
    const source = screen.getByRole("combobox", { name: "source" });
    expect(
      within(source)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["api", "prometheus"]);
  });

  // Pins that the plugin's `type` is not offered: one instance per type, the server checks it.
  it("hides the plugin's type", async () => {
    open({}, "hardware");
    await screen.findByLabelText("machines");
    expect(screen.queryByLabelText("type")).toBeNull();
    expect(document.querySelector('[data-path="plugins.hardware.type"]')).toBeNull();
  });

  // Pins that a locked switch's reason is reachable by keyboard: a focusable lock names it.
  it("puts a focusable lock naming the source beside a locked switch", async () => {
    const skip = envField(
      `${P}tls.insecureSkipVerify`,
      "boolean",
      "DASH_PLUGINS_PROXMOX_TLS_INSECURESKIPVERIFY",
      {
        value: true,
      },
    );
    const { user } = open({ [`GET ${settingsPaths.plugin("proxmox")}`]: proxmoxWith(skip) });
    const sw = await screen.findByRole("switch", { name: "insecureSkipVerify" });
    expect(sw).toBeDisabled();
    screen.getByLabelText("caFile").focus();
    await user.tab();
    expect(
      screen.getByRole("button", {
        name: /^Locked: .*DASH_PLUGINS_PROXMOX_TLS_INSECURESKIPVERIFY/,
      }),
    ).toHaveFocus();
  });

  // Pins that a long doc shows its first sentence and the rest behind a "More" disclosure.
  it("shows a long doc's first sentence and the rest behind More", async () => {
    const { user } = open();
    const more = await screen.findByRole("button", { name: "More about node" });
    const doc = more.parentElement?.querySelector("p");
    expect(doc).toHaveTextContent(/^The Proxmox node to read\.$/);
    expect(more).toHaveAttribute("aria-expanded", "false");
    await user.click(more);
    const less = screen.getByRole("button", { name: "Less about node" });
    expect(less).toHaveAttribute("aria-expanded", "true");
    expect(doc).toHaveTextContent(/^The Proxmox node to read\. Empty is discovered/);
  });

  it("keeps a locked field read-only and shows what locks it", async () => {
    const { stub, user } = open();
    const tokenID = await screen.findByLabelText("tokenID");
    expect(tokenID).toHaveAttribute("readonly");
    expect(tokenID).toHaveAccessibleDescription(
      expect.stringContaining("Locked: set in file /data/config.yaml"),
    );
    await user.type(tokenID, "x{Enter}");
    expect(tokenID).toHaveValue("dash@pve!dash");
    expect(screen.getByRole("status")).toHaveTextContent("Nothing to save.");
    expect(stub.callsTo(PLUGIN_PUT)).toHaveLength(0);
  });

  it("offers Remove stored value on a shadowed field", async () => {
    const node = envField(`${P}node`, "string", "DASH_PLUGINS_PROXMOX_NODE", {
      value: "pve-env",
      shadowed: true,
      stored: true,
    });
    const stored = `DELETE ${settingsPaths.stored("proxmox", "node")}`;
    const { stub, user } = open({
      [`GET ${settingsPaths.plugin("proxmox")}`]: proxmoxWith(node),
      [stored]: { revision: REVISION + 1, restartRequired: [] },
    });
    expect(await screen.findByLabelText("node")).toHaveAccessibleDescription(
      expect.stringContaining("overridden by env; stored value unused"),
    );
    await user.click(screen.getByRole("button", { name: "Remove stored value of node" }));
    await waitFor(() => expect(stub.callsTo(stored)).toHaveLength(1));
    expect(stub.callsTo(stored)[0]?.headers.get("If-Match")).toBe(`"${REVISION}"`);
  });

  // Pins that a stored, unlocked field offers "Reset to default": a DELETE of its stored value with
  // If-Match, then a fresh read; a field that is not stored or is locked offers none.
  it.each([
    ["a string", field(`${P}node`, "string", { value: "pve", set: true, source: "ui" })],
    [
      "an enum",
      field(`${P}storage.source`, "string", { value: "prometheus", enum: ["api", "prometheus"] }),
    ],
    ["a boolean", field(`${P}tls.insecureSkipVerify`, "boolean", { value: true })],
    ["a list", field(`${P}criticalVMs.ids`, "array", { value: [101] })],
  ])("resets %s stored from the UI to its default", async (_, f) => {
    const key = f.path.split(".").at(-1) ?? "";
    const rel = f.path.slice(P.length);
    const del = `DELETE ${settingsPaths.stored("proxmox", rel)}`;
    const get = `GET ${settingsPaths.plugin("proxmox")}`;
    const { stub, user } = open({
      [get]: proxmoxWith({ ...f, set: true, source: "ui", stored: true }),
      [del]: { revision: REVISION + 1, restartRequired: [] },
    });
    const button = await screen.findByRole("button", { name: `Reset ${key} to default` });
    expect(screen.queryByRole("button", { name: "Reset tokenID to default" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reset caFile to default" })).toBeNull();
    button.focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(stub.callsTo(del)).toHaveLength(1));
    expect(stub.callsTo(del)[0]?.headers.get("If-Match")).toBe(`"${REVISION}"`);
    await waitFor(() => expect(stub.callsTo(get)).toHaveLength(2));
  });

  it("resets a stored number and a stored JSON list to their defaults", async () => {
    const put = `PUT ${settingsPaths.plugin("hardware")}`;
    const delWarn = `DELETE ${settingsPaths.stored("hardware", "tempThresholds.warn")}`;
    const delMachines = `DELETE ${settingsPaths.stored("hardware", "machines")}`;
    const view = hardwareView();
    const ui = { set: true, source: "ui", stored: true } as const;
    view.fields = [
      ...view.fields.map((f) =>
        f.path === "plugins.hardware.machines" ? { ...f, ...ui, value: [{ name: "nas" }] } : f,
      ),
      field("plugins.hardware.tempThresholds.warn", "integer", { ...ui, value: 75 }),
    ];
    const ok = { revision: REVISION + 1, restartRequired: [] };
    const { stub, user } = open(
      {
        [`GET ${settingsPaths.plugin("hardware")}`]: view,
        [put]: ok,
        [delWarn]: ok,
        [delMachines]: ok,
      },
      "hardware",
    );
    await user.click(await screen.findByRole("button", { name: "Reset warn to default" }));
    await waitFor(() => expect(stub.callsTo(delWarn)).toHaveLength(1));
    await user.click(screen.getByRole("button", { name: "Reset machines to default" }));
    await waitFor(() => expect(stub.callsTo(delMachines)).toHaveLength(1));
    expect(screen.queryByRole("button", { name: "Reset hosts to default" })).toBeNull();
  });

  it("edits an array of objects as JSON and refuses JSON that is not a list of objects", async () => {
    const put = `PUT ${settingsPaths.plugin("hardware")}`;
    const { stub, user } = open(
      { [put]: { revision: REVISION + 1, restartRequired: [] } },
      "hardware",
    );
    const hosts = await screen.findByLabelText("hosts");
    expect(hosts).toHaveAttribute("readonly");
    expect(JSON.parse((hosts as HTMLTextAreaElement).value)).toEqual([
      { nodename: "pve1", machine: "r730" },
    ]);

    const machines = screen.getByLabelText("machines");
    await user.clear(machines);
    await user.click(machines);
    await user.paste('[{"name": "nas"');
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(machines).toHaveAccessibleDescription(expect.stringContaining("not valid JSON"));
    expect(screen.getByRole("alert")).toBeInTheDocument();

    await user.clear(machines);
    await user.paste('["nas"]');
    await user.click(screen.getByRole("button", { name: "Save" }));
    expect(machines).toHaveAccessibleDescription(expect.stringContaining("JSON list of objects"));
    expect(stub.callsTo(put)).toHaveLength(0);

    await user.clear(machines);
    await user.paste('[{"name": "nas", "gatusEndpoint": "nas"}]');
    await user.click(screen.getByRole("button", { name: "Save" }));
    await saved();
    expect(stub.callsTo(put).map((c) => c.body)).toEqual([
      { machines: [{ name: "nas", gatusEndpoint: "nas" }] },
    ]);
  });

  // Pins D16 as the server applies it: any raw change of a format: uri leaf moves the destination,
  // with no URL parsing in the browser (a scheme, a path, an IP literal or an IDN spelled another
  // way all count), so the set secret is asked for again and sent in the same PUT.
  it.each([
    ["a host change", "https://pve.example.com:8006", "https://pve2.example.com:8006"],
    ["a scheme-only change", "https://pve.example.com:8006", "http://pve.example.com:8006"],
    ["a path-only change", "https://pve.example.com:8006", "https://pve.example.com:8006/api"],
    ["an expanded IPv6 literal", "https://[::1]:8006", "https://[0:0:0:0:0:0:0:1]:8006"],
    ["a short IPv4", "https://127.0.0.1:8006", "https://127.1:8006"],
    ["an IDN in punycode", "https://bücher.example:8006", "https://xn--bcher-kva.example:8006"],
  ])("asks for a set secret again on %s, then sends both in one PUT", async (_, from, to) => {
    const url = proxmoxFields().find((f) => f.path === `${P}url`);
    if (!url) throw new Error("no url field");
    const { stub, user } = open({
      [`GET ${settingsPaths.plugin("proxmox")}`]: proxmoxWith({ ...url, value: from }),
    });
    const input = await screen.findByLabelText("url");
    await user.clear(input);
    await user.click(input);
    await user.paste(to);
    await user.keyboard("{Enter}");
    const secret = screen.getByRole("group", { name: "tokenSecret" });
    expect(secret).toHaveAccessibleDescription(expect.stringContaining(REENTER));
    expect(stub.callsTo(PLUGIN_PUT)).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Replace tokenSecret" }));
    await user.type(screen.getByLabelText("New tokenSecret"), "again{Enter}");
    await saved();
    expect(stub.callsTo(PLUGIN_PUT).map((c) => c.body)).toEqual([
      { url: to, tokenSecret: "again" },
    ]);
    expect(stub.callsTo(SECRET_PUT)).toHaveLength(0);
  });

  it("sends a typed secret through the secrets route when the URL is unchanged", async () => {
    const { stub, user } = open();
    await screen.findByLabelText("url");
    await user.click(screen.getByRole("button", { name: "Replace tokenSecret" }));
    await user.type(screen.getByLabelText("New tokenSecret"), "again{Enter}");
    await saved();
    expect(stub.callsTo(SECRET_PUT)).toHaveLength(1);
    expect(stub.callsTo(PLUGIN_PUT)).toHaveLength(0);
  });

  it("names the secret of a 409 secret_locked", async () => {
    const tokenSecret = envField(`${P}tokenSecret`, "string", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET", {
      secret: true,
    });
    const { user } = open({
      [`GET ${settingsPaths.plugin("proxmox")}`]: proxmoxWith(tokenSecret),
      [PLUGIN_PUT]: secretLocked(`${P}tokenSecret`),
    });
    const caFile = await screen.findByLabelText("caFile");
    expect(screen.queryByRole("button", { name: "Replace tokenSecret" })).toBeNull();
    await user.type(caFile, "/etc/ca.pem{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent(`${P}tokenSecret`);
    expect(screen.getByRole("group", { name: "tokenSecret" })).toHaveAccessibleDescription(
      expect.stringContaining("Set outside the UI"),
    );
  });

  it("sends a number as a number and refuses one that is not", async () => {
    const { stub, user } = open(
      {
        [`PUT ${settingsPaths.plugin("hardware")}`]: {
          revision: REVISION + 1,
          restartRequired: [],
        },
      },
      "hardware",
    );
    const warn = await screen.findByLabelText("warn");
    await user.type(warn, "7.5{Enter}");
    expect(warn).toHaveAccessibleDescription(expect.stringContaining("whole number"));
    await user.clear(warn);
    await user.type(warn, "75{Enter}");
    await saved();
    expect(stub.callsTo(`PUT ${settingsPaths.plugin("hardware")}`).map((c) => c.body)).toEqual([
      { tempThresholds: { warn: 75 } },
    ]);
  });
});
