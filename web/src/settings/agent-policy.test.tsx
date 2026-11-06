import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SettingField, SettingsPluginView } from "@/api/types";
import { renderApp, stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import {
  envField,
  field,
  patchResult,
  REVISION,
  settingsPlugin,
  settingsRoutes,
  settingsView,
  staleRevision,
} from "./fixtures";
import { McpPage } from "./mcp-page";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

const K = "plugins.kubernetes.actions.";
const PUT_K = `PUT ${settingsPaths.plugin("kubernetes")}`;
const GET_K = `GET ${settingsPaths.plugin("kubernetes")}`;
const GET_F = `GET ${settingsPaths.plugin("flux")}`;

/** GET /api/v1/settings/plugins/{id} with only the actions leaves. */
function pluginView(
  id: string,
  agents: SettingField = field(`plugins.${id}.actions.agents`, "string", { value: "approve" }),
  actionsOn = true,
): SettingsPluginView {
  return {
    id,
    schema: {},
    fields: [
      field(`plugins.${id}.actions.enabled`, "boolean", {
        value: actionsOn,
        set: true,
        source: "file",
      }),
      agents,
    ],
    revision: REVISION,
    testable: false,
  };
}

/** The settings of a dash with kubernetes and flux (and proxmox, which has no agent policy). */
function viewWithPolicyPlugins(readonly = false) {
  const v = settingsView();
  return {
    ...v,
    store: { ...v.store, readonly },
    plugins: [
      ...v.plugins,
      settingsPlugin("kubernetes", { name: "Kubernetes" }),
      settingsPlugin("flux", { name: "Flux" }),
    ],
  };
}

function routes(over: Record<string, unknown> = {}) {
  return settingsRoutes({
    [`GET ${settingsPaths.view}`]: viewWithPolicyPlugins(),
    [GET_K]: pluginView("kubernetes"),
    [GET_F]: pluginView("flux"),
    [PUT_K]: patchResult(),
    ...over,
  });
}

const group = (name: string) => screen.findByRole("group", { name: new RegExp(`^${name}`) });

describe("AgentPolicyPanel", () => {
  it("explains the three policies and shows each plugin's choice with its source", async () => {
    stubFetch(
      routes({ [GET_F]: pluginView("flux", field("plugins.flux.actions.agents", "string")) }),
    );
    renderApp(<McpPage />);
    const kubernetes = await group("Kubernetes");
    const approve = within(kubernetes).getByRole("radio", { name: "Approve" });
    expect(approve).toBeChecked();
    // Each choice is named by its label alone and described by its note.
    expect(approve).toHaveAccessibleDescription("each call waits for you to approve it here");
    expect(within(kubernetes).getByRole("radio", { name: "Allow" })).toHaveAccessibleDescription(
      "runs at once, except on protected targets",
    );
    expect(within(kubernetes).getByRole("radio", { name: "Deny" })).toHaveAccessibleDescription(
      "agents get none of the plugin's action tools",
    );
    expect(within(kubernetes).getByText("default")).toBeInTheDocument();
    // An unset value is the default, approve.
    expect(within(await group("Flux")).getByRole("radio", { name: /^Approve/ })).toBeChecked();
    expect(screen.queryByRole("group", { name: /^Proxmox/ })).toBeNull();
    expect(screen.getByText(/Approve asks you/)).toBeInTheDocument();
    expect(screen.getByText(/except on protected targets, which always ask/)).toBeInTheDocument();
    expect(screen.getByText(/Deny hides the plugin's action tools/)).toBeInTheDocument();
  });

  it("writes the policy through PUT with If-Match, then reads the settings again", async () => {
    const user = userEvent.setup();
    let agents = "approve";
    const stub = stubFetch(
      routes({
        [GET_K]: () =>
          pluginView(
            "kubernetes",
            field(`${K}agents`, "string", { value: agents, set: true, source: "ui" }),
          ),
        [PUT_K]: () => {
          agents = "allow";
          return patchResult();
        },
      }),
    );
    renderApp(<McpPage />);
    await user.click(within(await group("Kubernetes")).getByRole("radio", { name: /^Allow/ }));

    await waitFor(() => expect(stub.callsTo(PUT_K)).toHaveLength(1));
    const [put] = stub.callsTo(PUT_K);
    expect(put?.headers.get("If-Match")).toBe(`"${REVISION}"`);
    expect(put?.body).toEqual({ actions: { agents: "allow" } });
    expect(toast.success).toHaveBeenCalledWith("Kubernetes: agent actions set to allow");
    await waitFor(() =>
      expect(
        within(screen.getByRole("group", { name: /^Kubernetes/ })).getByRole("radio", {
          name: /^Allow/,
        }),
      ).toBeChecked(),
    );
    // A value saved from here says so.
    expect(
      within(screen.getByRole("group", { name: /^Kubernetes/ })).getByText("set here"),
    ).toBeInTheDocument();
  });

  it("reports a refused write and keeps the stored choice", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes({ [PUT_K]: () => staleRevision() }));
    renderApp(<McpPage />);
    await user.click(within(await group("Kubernetes")).getByRole("radio", { name: /^Deny/ }));
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("the settings changed; reload them"),
    );
    await waitFor(() => expect(stub.callsTo(GET_K).length).toBeGreaterThan(1));
    expect(
      within(screen.getByRole("group", { name: /^Kubernetes/ })).getByRole("radio", {
        name: /^Approve/,
      }),
    ).toBeChecked();
  });

  it("locks a value the environment sets, and says how to change it", async () => {
    const env = "DASH_PLUGINS_KUBERNETES_ACTIONS_AGENTS";
    const stub = stubFetch(
      routes({
        [GET_K]: pluginView("kubernetes", envField(`${K}agents`, "string", env, { value: "deny" })),
      }),
    );
    renderApp(<McpPage />);
    const kubernetes = await group("Kubernetes");
    const deny = within(kubernetes).getByRole("radio", { name: /^Deny/ });
    expect(deny).toBeChecked();
    expect(deny).toBeDisabled();
    expect(kubernetes).toHaveAccessibleDescription(
      `Set by the environment variable ${env}. To change it here, unset the variable and restart dash.`,
    );
    expect(within(kubernetes).getAllByText(new RegExp(env))).toHaveLength(1);
    expect(stub.callsTo(PUT_K)).toHaveLength(0);
  });

  it("locks every choice of a read-only store", async () => {
    stubFetch(routes({ [`GET ${settingsPaths.view}`]: viewWithPolicyPlugins(true) }));
    renderApp(<McpPage />);
    const flux = await group("Flux");
    expect(within(flux).getByRole("radio", { name: /^Allow/ })).toBeDisabled();
    expect(flux).toHaveAccessibleDescription(
      "Settings are read-only. Set plugins.flux.actions.agents in the config file, applied without a restart, or with DASH_PLUGINS_FLUX_ACTIONS_AGENTS and a restart.",
    );
  });

  it("says when the plugin's actions are off", async () => {
    stubFetch(
      routes({
        [GET_F]: pluginView("flux", field("plugins.flux.actions.agents", "string"), false),
      }),
    );
    renderApp(<McpPage />);
    expect(
      within(await group("Flux")).getByText(/actions are off \(plugins\.flux\.actions\.enabled\)/),
    ).toBeInTheDocument();
    expect(within(await group("Kubernetes")).queryByText(/actions are off/)).toBeNull();
  });

  it("shows nothing, and reads no plugin, on a dash without those plugins", async () => {
    const stub = stubFetch(settingsRoutes());
    renderApp(<McpPage />);
    expect(await screen.findByText("MCP server")).toBeInTheDocument();
    await waitFor(() => expect(stub.callsTo(`GET ${settingsPaths.view}`)).toHaveLength(1));
    expect(screen.queryByText("Agent actions")).toBeNull();
    expect(stub.callsTo(GET_K)).toHaveLength(0);
  });
});
