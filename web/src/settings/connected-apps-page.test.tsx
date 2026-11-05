import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Me, OAuthGrantInfo } from "@/api/types";
import { renderApp, stubFetch } from "@/test/harness";
import { ConnectedAppsPage } from "./connected-apps-page";
import { me, settingsError } from "./fixtures";

const LIST = "GET /api/v1/oauth/grants";
const REVOKE = "DELETE /api/v1/oauth/grants/g1";
const REVOKE_ALL = "DELETE /api/v1/oauth/grants";

function grant(over: Partial<OAuthGrantInfo> = {}): OAuthGrantInfo {
  return {
    id: "g1",
    user: "alice",
    client: { id: "https://agent.test/client.json", name: "Agent", kind: "metadata" },
    audience: "mcp",
    scopes: ["read", "logs"],
    createdAt: "2026-11-01T10:00:00Z",
    expiresAt: "2027-04-30T10:00:00Z",
    lastUsedAt: null,
    ...over,
  };
}

/** alice in `mode` on a dash whose OAuth server is on. */
function meWith(role: "admin" | "viewer", over: Partial<Me> = {}): Me {
  const base = me(role, { authMode: "oidc" });
  return { ...base, capabilities: { ...base.capabilities, tokens: true, oauth: true }, ...over };
}

function routes(who: Me, over: Record<string, unknown> = {}) {
  return {
    "GET /api/v1/me": who,
    [LIST]: { grants: [grant()] },
    [REVOKE]: undefined,
    [REVOKE_ALL]: undefined,
    ...over,
  };
}

describe("ConnectedAppsPage", () => {
  it("lists the connected apps with client, audience, scopes and use", async () => {
    stubFetch(
      routes(meWith("viewer"), {
        [LIST]: {
          grants: [
            grant(),
            grant({
              id: "g2",
              client: { id: "dash-android", name: "dash for Android", kind: "builtin" },
              audience: "api",
              scopes: ["read", "push"],
              lastUsedAt: "2026-11-04T08:00:00Z",
            }),
          ],
        },
      }),
    );
    renderApp(<ConnectedAppsPage />);
    const rows = (await screen.findAllByRole("row")).slice(1);
    expect(rows).toHaveLength(2);
    const [agent, phone] = rows as [HTMLElement, HTMLElement];
    expect(within(agent).getByText("Agent")).toBeInTheDocument();
    expect(within(agent).getByText("https://agent.test/client.json")).toBeInTheDocument();
    expect(within(agent).getByText("Unverified · metadata from agent.test")).toBeInTheDocument();
    expect(within(phone).getByText("Built in")).toBeInTheDocument();
    expect(within(agent).getByText("agents through MCP")).toBeInTheDocument();
    expect(within(agent).getByText("read, logs")).toBeInTheDocument();
    expect(within(agent).getByText("never")).toBeInTheDocument();
    expect(within(phone).getByText("the dash API")).toBeInTheDocument();
    expect(within(phone).getByText("read, push")).toBeInTheDocument();
    expect(within(phone).queryByText("never")).toBeNull();
    // A viewer sees only their own, and no user column.
    expect(screen.queryByRole("group", { name: "Whose connected apps" })).toBeNull();
    expect(screen.queryByRole("columnheader", { name: "User" })).toBeNull();
  });

  it("asks before revoking, and deletes only after the confirmation", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes(meWith("viewer")));
    renderApp(<ConnectedAppsPage />);
    await user.click(await screen.findByRole("button", { name: "Revoke Agent" }));
    const confirm = await screen.findByRole("alertdialog");
    expect(within(confirm).getByText("Revoke Agent?")).toBeInTheDocument();
    await user.click(within(confirm).getByRole("button", { name: "Keep" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(stub.callsTo(REVOKE)).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Revoke Agent" }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Revoke" }),
    );
    await waitFor(() => expect(stub.callsTo(REVOKE)).toHaveLength(1));
    expect(stub.callsTo(REVOKE)[0]?.headers.get("X-Dash-Csrf")).toBe("1");
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    // The list is read again after the revoke.
    await waitFor(() => expect(stub.callsTo(LIST).length).toBeGreaterThan(1));
  });

  it("keeps the dialog open with the reason when the revoke fails", async () => {
    const user = userEvent.setup();
    stubFetch(
      routes(meWith("viewer"), {
        [REVOKE]: settingsError(503, "audit_unavailable", "the audit log cannot be written"),
      }),
    );
    renderApp(<ConnectedAppsPage />);
    await user.click(await screen.findByRole("button", { name: "Revoke Agent" }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Revoke" }),
    );
    await waitFor(() =>
      expect(
        within(screen.getByRole("alertdialog")).getByRole("button", { name: "Revoke" }),
      ).toBeEnabled(),
    );
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
  });

  it("lets an administrator list one user's or every user's apps", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes(meWith("admin")));
    renderApp(<ConnectedAppsPage />);
    const group = await screen.findByRole("group", { name: "Whose connected apps" });
    await waitFor(() => expect(stub.callsTo(LIST)).toHaveLength(1));
    expect(stub.callsTo(LIST)[0]?.query.has("user")).toBe(false);

    await user.click(within(group).getByRole("button", { name: "All users" }));
    await waitFor(() => expect(stub.callsTo(LIST).at(-1)?.query.get("user")).toBe("*"));
    expect(await screen.findByRole("columnheader", { name: "User" })).toBeInTheDocument();

    await user.click(within(group).getByRole("button", { name: "One user" }));
    expect(screen.getByText("Type a user name, then Show.")).toBeInTheDocument();
    await user.type(screen.getByLabelText("User name"), "bob");
    await user.click(screen.getByRole("button", { name: "Show" }));
    await waitFor(() => expect(stub.callsTo(LIST).at(-1)?.query.get("user")).toBe("bob"));
  });

  it("revokes all of the caller's apps in the proxy mode, after a confirmation", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes(meWith("viewer", { authMode: "proxy" })));
    renderApp(<ConnectedAppsPage />);
    await user.click(await screen.findByRole("button", { name: "Revoke all my connected apps" }));
    const confirm = await screen.findByRole("alertdialog");
    expect(within(confirm).getByText(/Every app you connected/)).toBeInTheDocument();
    expect(stub.callsTo(REVOKE_ALL)).toHaveLength(0);
    await user.click(within(confirm).getByRole("button", { name: "Revoke all" }));
    await waitFor(() => expect(stub.callsTo(REVOKE_ALL)).toHaveLength(1));
    const [call] = stub.callsTo(REVOKE_ALL);
    expect(call?.query.get("user")).toBe("me");
    expect(call?.headers.get("X-Dash-Csrf")).toBe("1");
  });

  it.each(["oidc", "local"])("has no Revoke all button in the %s mode", async (authMode) => {
    stubFetch(routes(meWith("admin", { authMode })));
    renderApp(<ConnectedAppsPage />);
    expect(await screen.findByText("Agent")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Revoke all my connected apps" })).toBeNull();
  });

  it.each([
    [
      "the OAuth server is off",
      meWith("admin", { capabilities: { ...me("admin").capabilities, tokens: true } }),
      /does not authorize apps/,
    ],
    ["the none mode", meWith("admin", { authMode: "none" }), /no user accounts/],
    [
      "the anonymous viewer",
      meWith("viewer", { authMode: "local", user: "anonymous" }),
      /Sign in to manage/,
    ],
  ])("says so, and reads nothing, for %s", async (_, who, says) => {
    const stub = stubFetch(routes(who));
    renderApp(<ConnectedAppsPage />);
    expect(await screen.findByText(says)).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
    expect(stub.callsTo(LIST)).toHaveLength(0);
  });
});
