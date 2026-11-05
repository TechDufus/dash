import { screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { Me, TokenCreated, TokenInfo } from "@/api/types";
import { json, renderApp, stubFetch } from "@/test/harness";
import { me, settingsError } from "./fixtures";
import { TokensPage } from "./tokens-page";

const LIST = "GET /api/v1/tokens";
const CREATE = "POST /api/v1/tokens";
const SECRET = "dash_pat_ABCDEFGHIJ_0123456789abcdefghijklmnopqrstuvwxyzABCDEFG";

function token(over: Partial<TokenInfo> = {}): TokenInfo {
  return {
    id: "ABCDEFGHIJ",
    name: "ci",
    user: "alice",
    audience: "api",
    scopes: ["read"],
    actionNamespaces: [],
    createdAt: "2026-11-01T10:00:00Z",
    expiresAt: "2099-01-01T00:00:00Z",
    lastUsedAt: null,
    ...over,
  };
}

/** alice in the proxy mode with a data dir that keeps tokens. */
function meWith(role: "admin" | "viewer", over: Partial<Me> = {}): Me {
  const base = me(role);
  return {
    ...base,
    scopes: role === "admin" ? ["read", "logs", "actions:kubernetes"] : ["read", "logs"],
    capabilities: { ...base.capabilities, tokens: true },
    ...over,
  };
}

function routes(who: Me, over: Record<string, unknown> = {}) {
  return {
    "GET /api/v1/me": who,
    [LIST]: { tokens: [token()] },
    [CREATE]: () =>
      json(
        { token: SECRET, info: token({ id: "UVWXYZ2345", name: "new" }) } satisfies TokenCreated,
        201,
      ),
    "DELETE /api/v1/tokens/ABCDEFGHIJ": undefined,
    ...over,
  };
}

async function openCreate(user: UserEvent) {
  await user.click(await screen.findByRole("button", { name: "New token" }));
  return screen.findByRole("dialog");
}

describe("TokensPage", () => {
  it("lists the tokens with their scopes, dates and an expired marker", async () => {
    stubFetch(
      routes(meWith("viewer"), {
        [LIST]: {
          tokens: [
            token({
              name: "grafana",
              audience: "mcp",
              scopes: ["read", "logs"],
              lastUsedAt: "2026-11-04T08:00:00Z",
            }),
            token({ id: "KLMNOPQRST", name: "old", expiresAt: "2026-01-01T00:00:00Z" }),
          ],
        },
      }),
    );
    renderApp(<TokensPage />);
    const rows = (await screen.findAllByRole("row")).slice(1);
    expect(rows).toHaveLength(2);
    const [fresh, old] = rows as [HTMLElement, HTMLElement];
    expect(within(fresh).getByText("grafana")).toBeInTheDocument();
    expect(within(fresh).getByText("mcp")).toBeInTheDocument();
    expect(within(fresh).getByText("read, logs")).toBeInTheDocument();
    expect(within(fresh).queryByText("expired")).not.toBeInTheDocument();
    expect(within(fresh).queryByText("never")).not.toBeInTheDocument();
    expect(within(old).getByText("never")).toBeInTheDocument();
    expect(within(old).getByText("expired")).toBeInTheDocument();
  });

  it("shows a new token once, with a warning, and drops it when the dialog closes", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes(meWith("viewer")));
    renderApp(<TokensPage />);
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByLabelText("Name"), "laptop");
    await user.click(within(dialog).getByRole("button", { name: "Create token" }));

    expect(await screen.findByDisplayValue(SECRET)).toBeInTheDocument();
    expect(screen.getByText("This token is shown once")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Copy" })).toBeInTheDocument();
    const [post] = stub.callsTo(CREATE);
    expect(post?.body).toEqual({
      name: "laptop",
      audience: "api",
      scopes: ["read"],
      expiresInDays: 30,
    });
    expect(post?.headers.get("X-Dash-Csrf")).toBe("1");

    await user.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByDisplayValue(SECRET)).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain(SECRET);

    // Reopened, the dialog is a fresh form: the secret is gone for good.
    const again = await openCreate(user);
    expect(within(again).getByLabelText("Name")).toHaveValue("");
    expect(document.body.innerHTML).not.toContain(SECRET);
  });

  it("asks before revoking, and deletes only after the confirmation", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes(meWith("viewer")));
    renderApp(<TokensPage />);
    await user.click(await screen.findByRole("button", { name: "Revoke ci" }));
    const confirm = await screen.findByRole("alertdialog");
    expect(within(confirm).getByText("Revoke ci?")).toBeInTheDocument();
    expect(stub.callsTo("DELETE /api/v1/tokens/ABCDEFGHIJ")).toHaveLength(0);

    await user.click(within(confirm).getByRole("button", { name: "Keep" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(stub.callsTo("DELETE /api/v1/tokens/ABCDEFGHIJ")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "Revoke ci" }));
    await user.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Revoke" }),
    );
    await waitFor(() => expect(stub.callsTo("DELETE /api/v1/tokens/ABCDEFGHIJ")).toHaveLength(1));
    expect(stub.callsTo("DELETE /api/v1/tokens/ABCDEFGHIJ")[0]?.headers.get("X-Dash-Csrf")).toBe(
      "1",
    );
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
  });

  it("never shows a viewer action scopes or the user filter", async () => {
    const user = userEvent.setup();
    // Even a /me that listed an actions scope gives a viewer none to pick.
    const stub = stubFetch(
      routes(meWith("viewer", { scopes: ["read", "logs", "actions:kubernetes"] })),
    );
    renderApp(<TokensPage />);
    expect(await screen.findByText("ci")).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Whose tokens" })).not.toBeInTheDocument();
    expect(stub.callsTo(LIST)[0]?.query.has("user")).toBe(false);

    const dialog = await openCreate(user);
    expect(within(dialog).getByRole("checkbox", { name: "read" })).toBeChecked();
    expect(within(dialog).getByRole("checkbox", { name: "logs" })).not.toBeChecked();
    expect(within(dialog).queryByRole("checkbox", { name: /^actions:/ })).toBeNull();
    expect(within(dialog).queryByLabelText("Action namespaces")).toBeNull();
  });

  it("gives an administrator action scopes with namespaces and the user filter", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes(meWith("admin")));
    renderApp(<TokensPage />);
    const filter = await screen.findByRole("group", { name: "Whose tokens" });

    await user.click(within(filter).getByRole("button", { name: "All users" }));
    await waitFor(() => expect(stub.callsTo(LIST).at(-1)?.query.get("user")).toBe("*"));
    expect(await screen.findByRole("columnheader", { name: "User" })).toBeInTheDocument();

    await user.click(within(filter).getByRole("button", { name: "One user" }));
    await user.type(screen.getByLabelText("User name"), "bob");
    await user.click(screen.getByRole("button", { name: "Show" }));
    await waitFor(() => expect(stub.callsTo(LIST).at(-1)?.query.get("user")).toBe("bob"));

    await user.click(within(filter).getByRole("button", { name: "Mine" }));
    await waitFor(() => expect(stub.callsTo(LIST).at(-1)?.query.has("user")).toBe(false));

    const dialog = await openCreate(user);
    expect(within(dialog).queryByLabelText("Action namespaces")).toBeNull();
    await user.click(within(dialog).getByRole("checkbox", { name: "actions:kubernetes" }));
    await user.type(within(dialog).getByLabelText("Action namespaces"), "media, apps-*");
    await user.type(within(dialog).getByLabelText("Name"), "agent");
    await user.click(within(dialog).getByLabelText(/^mcp/));
    await user.click(within(dialog).getByRole("button", { name: "Create token" }));
    await screen.findByDisplayValue(SECRET);
    expect(stub.callsTo(CREATE)[0]?.body).toEqual({
      name: "agent",
      audience: "mcp",
      scopes: ["read", "actions:kubernetes"],
      actionNamespaces: ["media", "apps-*"],
      expiresInDays: 30,
    });
  });

  it("explains and disables creation without a store that keeps tokens", async () => {
    const stub = stubFetch(routes(me("viewer")));
    renderApp(<TokensPage />);
    expect(await screen.findByText("Tokens cannot be created here")).toBeInTheDocument();
    expect(screen.getByText(/writable data/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New token" })).toBeDisabled();
    // The list and revocation still work.
    expect(await screen.findByText("ci")).toBeInTheDocument();
    expect(stub.callsTo(LIST)).toHaveLength(1);
  });

  it("puts the server's field errors on their inputs", async () => {
    const user = userEvent.setup();
    stubFetch(
      routes(meWith("viewer"), {
        [CREATE]: settingsError(400, "invalid_input", "invalid token request", {
          fields: [
            { path: "name", message: "a token with this name exists" },
            { path: "expiresInDays", message: "must be 1 to 365" },
          ],
        }),
      }),
    );
    renderApp(<TokensPage />);
    const dialog = await openCreate(user);
    const name = within(dialog).getByLabelText("Name");
    await user.type(name, "ci");
    await user.click(within(dialog).getByRole("button", { name: "Create token" }));

    await waitFor(() => expect(name).toHaveAttribute("aria-invalid", "true"));
    expect(name).toHaveAccessibleDescription("a token with this name exists");
    const days = within(dialog).getByLabelText("Expires in (days)");
    expect(days).toHaveAttribute("aria-invalid", "true");
    expect(days).toHaveAccessibleDescription(/must be 1 to 365/);
    expect(within(dialog).queryByText("The token was not created")).toBeNull();
  });

  it.each([
    [409, "tokens_unavailable", "no writable data dir keeps a new token"],
    [503, "audit_unavailable", "the audit log cannot be written"],
  ])("shows the server's message for a %i", async (status, code, message) => {
    const user = userEvent.setup();
    stubFetch(routes(meWith("viewer"), { [CREATE]: settingsError(status, code, message) }));
    renderApp(<TokensPage />);
    const dialog = await openCreate(user);
    await user.type(within(dialog).getByLabelText("Name"), "ci");
    await user.click(within(dialog).getByRole("button", { name: "Create token" }));
    expect(await within(dialog).findByText(message)).toBeInTheDocument();
    expect(within(dialog).getByText("The token was not created")).toBeInTheDocument();
  });

  it("has no tokens in the none mode and reads none", async () => {
    const stub = stubFetch(routes(meWith("admin", { authMode: "none" })));
    renderApp(<TokensPage />);
    expect(await screen.findByText(/tokens are not available/)).toBeInTheDocument();
    expect(stub.callsTo(LIST)).toHaveLength(0);
  });
});
