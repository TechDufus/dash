import { fireEvent, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
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

    fireEvent.click(toggle());
    await waitFor(() => expect(screen.getByText("serving")).toBeInTheDocument());
    expect(toggle()).toBeChecked();
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

  it("cannot switch a value the environment sets, and says so", async () => {
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
    expect(toggle()).toHaveAccessibleDescription("Set by env DASH_MCP_ENABLED");
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
});
