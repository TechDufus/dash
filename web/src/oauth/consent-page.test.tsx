import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, type MockInstance, vi } from "vitest";
import type { Me, OAuthConsent } from "@/api/types";
import { browserNav } from "@/auth/handle401";
import { PluginRoutes } from "@/plugins/routes";
import { me as meOf, settingsError } from "@/settings/fixtures";
import { json, renderApp, stubFetch } from "@/test/harness";
import { ConsentPage } from "./consent-page";

const GET = "GET /api/v1/oauth/consents/abc";
const POST = "POST /api/v1/oauth/consents/abc";
const BACK = "http://127.0.0.1:43117/callback?code=c0de&state=s1&iss=https%3A%2F%2Fdash.test";

function consent(over: Partial<OAuthConsent> = {}): OAuthConsent {
  return {
    id: "abc",
    client: { id: "https://agent.test/client.json", name: "Agent", kind: "metadata" },
    redirectUri: "http://127.0.0.1:43117/callback",
    audience: "mcp",
    scopes: ["read", "logs", "actions:flux"],
    defaultScopes: ["read"],
    expiresAt: "2026-11-05T10:10:00Z",
    ...over,
  };
}

/** alice, an administrator in the oidc mode, on a dash that serves OAuth. */
function meWith(over: Partial<Me> = {}): Me {
  const base = meOf("admin", { authMode: "oidc" });
  return { ...base, capabilities: { ...base.capabilities, oauth: true }, ...over };
}

function routes(over: Record<string, unknown> = {}) {
  return {
    "GET /api/v1/me": meWith(),
    [GET]: consent(),
    [POST]: { redirect: BACK },
    ...over,
  };
}

let assign: MockInstance<(url: string) => void>;

beforeEach(() => {
  assign = vi.spyOn(browserNav, "assign").mockImplementation(() => {});
});

afterEach(() => {
  window.history.replaceState(null, "", "/");
  sessionStorage.clear();
});

describe("ConsentPage", () => {
  it("names the app, the host of its metadata, its kind and audience, and ticks only read", async () => {
    stubFetch(routes({ [GET]: consent({ audience: "api" }) }));
    renderApp(<ConsentPage id="abc" />);
    expect(
      await screen.findByRole("heading", { name: "Connect Agent from agent.test?" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Unverified · metadata from agent.test")).toBeInTheDocument();
    expect(screen.getByText("URL")).toBeInTheDocument();
    expect(screen.getByText("https://agent.test/client.json")).toBeInTheDocument();
    expect(screen.getAllByText("the dash API").length).toBeGreaterThan(0);
    expect(screen.getByRole("checkbox", { name: "read" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "logs" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "actions:flux" })).not.toBeChecked();
    // Only the built-in app is ever offered these.
    expect(screen.queryByRole("checkbox", { name: "approvals" })).toBeNull();
    expect(screen.queryByRole("checkbox", { name: "push" })).toBeNull();
  });

  it("offers approvals and push when the backend offers them (the built-in app)", async () => {
    stubFetch(
      routes({
        [GET]: consent({
          client: { id: "dash-android", name: "dash for Android", kind: "builtin" },
          audience: "api",
          scopes: ["read", "approvals", "push"],
        }),
      }),
    );
    renderApp(<ConsentPage id="abc" />);
    expect(await screen.findByText("Client ID")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Connect dash for Android?" })).toBeInTheDocument();
    expect(screen.getByText("Built in")).toBeInTheDocument();
    expect(screen.queryByText(/Unverified/)).toBeNull();
    expect(screen.getByText("dash-android")).toBeInTheDocument();
    expect(screen.getAllByText("the dash API").length).toBeGreaterThan(0);
    expect(screen.getByRole("checkbox", { name: "approvals" })).not.toBeChecked();
    expect(screen.getByRole("checkbox", { name: "push" })).not.toBeChecked();
  });

  it("badges a client the admin configured", async () => {
    stubFetch(
      routes({
        [GET]: consent({
          client: { id: "inspector", name: "Inspector", kind: "config" },
          audience: "api",
        }),
      }),
    );
    renderApp(<ConsentPage id="abc" />);
    expect(await screen.findByRole("heading", { name: "Connect Inspector?" })).toBeInTheDocument();
    expect(screen.getByText("Configured by your admin")).toBeInTheDocument();
  });

  it.each([
    [
      "mcp where dash serves MCP",
      "mcp",
      true,
      /each action waits for your approval/i,
      /without asking again/i,
    ],
    [
      "mcp before the MCP server",
      "mcp",
      false,
      /runs without asking again/i,
      /waits for your approval/i,
    ],
    ["api", "api", true, /runs without asking again/i, /waits for your approval/i],
  ])("warns about actions for %s", async (_name, audience, mcp, says, never) => {
    const base = meWith();
    stubFetch(
      routes({
        "GET /api/v1/me": { ...base, capabilities: { ...base.capabilities, mcp } },
        [GET]: consent({ audience }),
      }),
    );
    renderApp(<ConsentPage id="abc" />);
    const actions = await screen.findByRole("checkbox", { name: "actions:flux" });
    expect(actions).toHaveAccessibleDescription(says);
    expect(actions).not.toHaveAccessibleDescription(never);
    expect(screen.getByRole("checkbox", { name: "logs" })).toHaveAccessibleDescription(
      /secrets and personal data/,
    );
    expect(screen.getByRole("checkbox", { name: "read" })).not.toHaveAccessibleDescription(
      /secrets|approval|asking/,
    );
  });

  it("approves the ticked scopes with the CSRF header, then follows the redirect", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes());
    renderApp(<ConsentPage id="abc" />);
    await user.click(await screen.findByRole("checkbox", { name: "actions:flux" }));
    await user.click(screen.getByRole("checkbox", { name: "logs" }));
    await user.click(screen.getByRole("button", { name: "Approve" }));

    await waitFor(() => expect(assign).toHaveBeenCalledWith(BACK));
    const [post] = stub.callsTo(POST);
    // The offered order, not the order the boxes were ticked in.
    expect(post?.body).toEqual({ approve: true, scopes: ["read", "logs", "actions:flux"] });
    expect(post?.headers.get("X-Dash-Csrf")).toBe("1");
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Deny" })).toBeDisabled();
  });

  it("cannot approve with nothing ticked", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes());
    renderApp(<ConsentPage id="abc" />);
    await user.click(await screen.findByRole("checkbox", { name: "read" }));
    expect(screen.getByRole("button", { name: "Approve" })).toBeDisabled();
    expect(stub.callsTo(POST)).toHaveLength(0);
  });

  it("denies, then follows the redirect", async () => {
    const denied = "http://127.0.0.1:43117/callback?error=access_denied&state=s1&iss=x";
    const user = userEvent.setup();
    const stub = stubFetch(routes({ [POST]: { redirect: denied } }));
    renderApp(<ConsentPage id="abc" />);
    await user.click(await screen.findByRole("button", { name: "Deny" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith(denied));
    expect(stub.callsTo(POST)[0]?.body).toEqual({ approve: false });
  });

  it("says an expired or unknown request expired, and offers nothing to decide", async () => {
    stubFetch(routes({ [GET]: settingsError(404, "not_found", "not found") }));
    renderApp(<ConsentPage id="abc" />);
    expect(
      await screen.findByText("This request expired; start again from the app."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(assign).not.toHaveBeenCalled();
  });

  it("says the request expired when it ends while the screen is open", async () => {
    const user = userEvent.setup();
    stubFetch(routes({ [POST]: settingsError(404, "not_found", "not found") }));
    renderApp(<ConsentPage id="abc" />);
    await user.click(await screen.findByRole("button", { name: "Approve" }));
    expect(
      await screen.findByText("This request expired; start again from the app."),
    ).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
  });

  it("shows the server's message when the user holds the most grants", async () => {
    const message = "you have 20 connected apps; disconnect one first";
    const user = userEvent.setup();
    stubFetch(routes({ [POST]: settingsError(409, "grant_limit", message) }));
    renderApp(<ConsentPage id="abc" />);
    await user.click(await screen.findByRole("button", { name: "Approve" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText(message)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
    expect(assign).not.toHaveBeenCalled();
  });

  it("sends the anonymous viewer to sign in with the short next, and reads nothing", async () => {
    const stub = stubFetch(
      routes({
        "GET /api/v1/me": meWith({ authMode: "local", role: "viewer", user: "anonymous" }),
      }),
    );
    renderApp(<ConsentPage id="abc" />);
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/login?next=%2Fconsent%2Fabc"));
    expect(stub.callsTo(GET)).toHaveLength(0);
  });

  it("sends a signed-out user to sign in with the short next", async () => {
    window.history.replaceState(null, "", "/consent/abc");
    stubFetch(
      routes({
        "GET /api/v1/me": json(
          {
            error: "sign in",
            code: "unauthenticated",
            details: { mode: "local", setup: false },
          },
          401,
        ),
      }),
    );
    renderApp(<ConsentPage id="abc" />, { path: "/consent/abc" });
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/login?next=%2Fconsent%2Fabc"));
  });

  it("says so, and reads nothing, where dash serves no OAuth", async () => {
    const stub = stubFetch(routes({ "GET /api/v1/me": meOf("admin") }));
    renderApp(<ConsentPage id="abc" />);
    expect(await screen.findByText(/not enabled on this dash/)).toBeInTheDocument();
    expect(stub.callsTo(GET)).toHaveLength(0);
  });

  it("is a route of the app shell", async () => {
    stubFetch(routes());
    renderApp(<PluginRoutes />, { path: "/consent/abc" });
    expect(
      await screen.findByRole(
        "heading",
        { name: "Connect Agent from agent.test?" },
        { timeout: 3000 },
      ),
    ).toBeInTheDocument();
  });
});
