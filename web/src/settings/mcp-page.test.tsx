import { fireEvent, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderApp, stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import {
  envField,
  field,
  generalFields,
  me,
  patchResult,
  REVISION,
  settingsRoutes,
  settingsView,
  staleRevision,
} from "./fixtures";
import { MCP_ENABLED, McpPage } from "./mcp-page";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const PUT = `PUT ${settingsPaths.general}`;

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

/** The general fields with mcp.enabled replaced. */
function withMCP(f = field(MCP_ENABLED, "boolean", { value: false })) {
  return settingsView({
    general: [...generalFields().filter((g) => g.path !== MCP_ENABLED), f],
  });
}

const toggle = () => screen.getByRole("switch", { name: "Serve MCP at /mcp" });

describe("McpPage", () => {
  it("switches mcp.enabled through PUT general with If-Match and shows the live state", async () => {
    let on = false;
    const f = stubFetch(
      settingsRoutes({
        "GET /api/v1/me": () => {
          const m = me("admin");
          return { ...m, capabilities: { ...m.capabilities, mcp: on } };
        },
        [`GET ${settingsPaths.view}`]: () =>
          on
            ? withMCP(field(MCP_ENABLED, "boolean", { value: true, set: true, source: "ui" }))
            : withMCP(),
        [PUT]: () => {
          on = true;
          return patchResult();
        },
      }),
    );
    renderApp(<McpPage />);
    await waitFor(() => expect(toggle()).not.toBeChecked());
    expect(screen.getByText("not serving")).toBeInTheDocument();
    // MCP has action tools now (behind the agent policy): the card no longer says read-only.
    expect(screen.getByText(/^Tools, resources and prompts for AI clients/)).toBeInTheDocument();
    expect(screen.queryByText(/read-only/i)).toBeNull();

    fireEvent.click(toggle());
    await waitFor(() => expect(screen.getByText("serving")).toBeInTheDocument());
    expect(toggle()).toBeChecked();
    // A value stored from the UI says where it was set.
    expect(screen.getByText("set here")).toBeInTheDocument();
    const puts = f.callsTo(PUT);
    expect(puts.map((c) => [c.headers.get("If-Match"), c.body])).toEqual([
      [`"${REVISION}"`, { mcp: { enabled: true } }],
    ]);
    expect(toast.success).toHaveBeenCalledWith("MCP server enabled");
    // The live state comes from /me, read again after the write.
    expect(f.callsTo("GET /api/v1/me")).toHaveLength(2);
  });

  it("reports a refused write and reads the fresh revision for the next attempt", async () => {
    const f = stubFetch(
      settingsRoutes({
        [`GET ${settingsPaths.view}`]: withMCP(),
        [PUT]: () => staleRevision(),
      }),
    );
    renderApp(<McpPage />);
    await waitFor(() => expect(toggle()).toBeEnabled());
    fireEvent.click(toggle());

    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(toast.error).toHaveBeenCalledWith("the settings changed; reload them");
    await waitFor(() => expect(f.callsTo(`GET ${settingsPaths.view}`)).toHaveLength(2));
    expect(toggle()).not.toBeChecked();
  });

  it("cannot switch a value the environment sets, and says how to unlock it", async () => {
    stubFetch(
      settingsRoutes({
        [`GET ${settingsPaths.view}`]: withMCP(
          envField(MCP_ENABLED, "boolean", "DASH_MCP_ENABLED", { value: true }),
        ),
      }),
    );
    renderApp(<McpPage />);
    await waitFor(() => expect(toggle()).toBeDisabled());
    expect(toggle()).toBeChecked();
    expect(toggle()).toHaveAccessibleDescription(
      "Set by the environment variable DASH_MCP_ENABLED. To switch it here, unset the variable and restart dash.",
    );
    // The lock says where the value comes from once: no source label beside it.
    expect(screen.getAllByText(/DASH_MCP_ENABLED/)).toHaveLength(1);
    // A locked switch does not apply at once.
    expect(screen.queryByText(/applies at once/)).toBeNull();
  });

  it("says to edit the config file when it sets the value", async () => {
    stubFetch(
      settingsRoutes({
        [`GET ${settingsPaths.view}`]: withMCP(
          field(MCP_ENABLED, "boolean", {
            value: false,
            set: true,
            source: "file",
            ref: "/etc/dash/config.yaml",
            locked: true,
            lockedBy: "file",
          }),
        ),
      }),
    );
    renderApp(<McpPage />);
    await waitFor(() => expect(toggle()).toBeDisabled());
    expect(toggle()).toHaveAccessibleDescription(
      "Set in the config file /etc/dash/config.yaml. Change it there, or remove it to switch it here; dash applies the file without a restart.",
    );
    expect(screen.getAllByText(/\/etc\/dash\/config\.yaml/)).toHaveLength(1);
  });

  it("says the switch applies at once while it is unlocked, with its source", async () => {
    stubFetch(settingsRoutes({ [`GET ${settingsPaths.view}`]: withMCP() }));
    renderApp(<McpPage />);
    await waitFor(() => expect(toggle()).toBeEnabled());
    expect(screen.getByText(/The switch applies at once, without a restart\./)).toBeInTheDocument();
    expect(screen.getByText("default")).toBeInTheDocument();
  });

  it("shows how to connect: the endpoint, the Tokens page and a Claude Code command", async () => {
    stubFetch(settingsRoutes({ [`GET ${settingsPaths.view}`]: withMCP() }));
    renderApp(<McpPage />);
    expect(await screen.findByText("https://dash.example.com/mcp")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Settings → Tokens" })).toHaveAttribute(
      "href",
      "/settings/tokens",
    );
    expect(
      screen.getByText(
        'claude mcp add --transport http dash https://dash.example.com/mcp --header "Authorization: Bearer <token>"',
      ),
    ).toBeInTheDocument();
  });

  it("names the endpoint on server.directURL when it is set", async () => {
    const view = withMCP();
    view.general.push(
      envField("server.directURL", "string", "DASH_SERVER_DIRECT_URL", {
        value: "https://dash-direct.example.com",
        restart: true,
      }),
    );
    stubFetch(settingsRoutes({ [`GET ${settingsPaths.view}`]: view }));
    renderApp(<McpPage />);
    expect(await screen.findByText("https://dash-direct.example.com/mcp")).toBeInTheDocument();
    expect(
      screen.getByText(
        'claude mcp add --transport http dash https://dash-direct.example.com/mcp --header "Authorization: Bearer <token>"',
      ),
    ).toBeInTheDocument();
  });

  describe("Copy command", () => {
    const COMMAND =
      'claude mcp add --transport http dash https://dash.example.com/mcp --header "Authorization: Bearer <token>"';

    afterEach(() => {
      Reflect.deleteProperty(navigator, "clipboard");
      Reflect.deleteProperty(document, "execCommand");
    });

    async function render() {
      stubFetch(settingsRoutes({ [`GET ${settingsPaths.view}`]: withMCP() }));
      renderApp(<McpPage />);
      await screen.findByText(COMMAND);
    }

    it("copies the command with writeText", async () => {
      const writeText = vi.fn(async (_: string) => {});
      Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
      await render();
      fireEvent.click(screen.getByRole("button", { name: "Copy command" }));
      expect(writeText).toHaveBeenCalledWith(COMMAND);
      await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Command copied"));
    });

    it("copies the shown command with execCommand inside the click without the clipboard API", async () => {
      Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
      const copied: string[] = [];
      const exec = vi.fn((command: string) => {
        if (command === "copy") copied.push(String(document.getSelection()));
        return true;
      });
      Object.defineProperty(document, "execCommand", { value: exec, configurable: true });
      await render();
      fireEvent.click(screen.getByRole("button", { name: "Copy command" }));
      expect(copied).toEqual([COMMAND]);
      expect(toast.success).toHaveBeenCalledWith("Command copied");
    });
  });
});
