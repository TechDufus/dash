import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, type MockInstance, vi } from "vitest";
import type { Me } from "@/api/types";
import { json, renderApp, stubFetch } from "@/test/harness";
import { browserNav } from "./handle401";
import { UserMenu } from "./user-menu";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const LOGOUT = "POST /auth/logout";

const me = (over: Partial<Me> = {}): Me => ({
  user: "alice",
  role: "admin",
  authMode: "local",
  scopes: ["read"],
  audience: "api",
  version: "test",
  capabilities: { actions: false, mcp: false, push: false, oauth: false, tokens: false },
  ui: { iconBaseUrl: "" },
  warnings: [],
  ...over,
});

let assign: MockInstance<(url: string) => void>;

beforeEach(() => {
  assign = vi.spyOn(browserNav, "assign").mockImplementation(() => {});
  vi.mocked(toast.error).mockClear();
});

async function openMenu(m: Me) {
  renderApp(<UserMenu me={m} />);
  const u = userEvent.setup();
  await u.click(screen.getByRole("button", { name: `Account: ${m.user}` }));
  await screen.findByRole("menu");
  return u;
}

describe("UserMenu", () => {
  it.each(["proxy", "none", "dev", "demo", "", "LOCAL"])("renders nothing in %j mode", (mode) => {
    const view = renderApp(<UserMenu me={me({ authMode: mode })} />);
    expect(view.container).toBeEmptyDOMElement();
  });

  it.each([
    ["local", "admin"],
    ["oidc", "viewer"],
  ])("shows the name and role in %s mode (%s)", async (mode, role) => {
    await openMenu(me({ authMode: mode, user: "alice", role }));
    const menu = screen.getByRole("menu");
    expect(menu).toHaveTextContent("alice");
    expect(menu).toHaveTextContent(role);
    expect(screen.getByRole("menuitem", { name: "Log out" })).toBeInTheDocument();
  });

  it("gives the local viewer (the anonymous viewer) a Sign in link back to this page", () => {
    window.history.replaceState(null, "", "/pods?ns=media");
    try {
      renderApp(<UserMenu me={me({ authMode: "local", user: "anonymous", role: "viewer" })} />);
      expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute(
        "href",
        `/login?next=${encodeURIComponent("/pods?ns=media")}`,
      );
      expect(screen.queryByRole("button", { name: /Account/ })).toBeNull();
    } finally {
      window.history.replaceState(null, "", "/");
    }
  });

  it("has no Sign in link for the local administrator", () => {
    renderApp(<UserMenu me={me({ authMode: "local", role: "admin" })} />);
    expect(screen.queryByRole("link", { name: "Sign in" })).toBeNull();
    expect(screen.getByRole("button", { name: "Account: alice" })).toBeInTheDocument();
  });

  it.each(["proxy", "oidc"])("has no Sign in link for a %s viewer", (mode) => {
    renderApp(<UserMenu me={me({ authMode: mode, role: "viewer" })} />);
    expect(screen.queryByRole("link", { name: "Sign in" })).toBeNull();
  });

  it.each(["admin", "viewer"] as const)("links a signed-in %s to their tokens", async (role) => {
    await openMenu(me({ authMode: "oidc", role }));
    expect(screen.getByRole("menuitem", { name: "Tokens" })).toHaveAttribute(
      "href",
      "/settings/tokens",
    );
  });

  it("offers Log out everywhere to an administrator only", async () => {
    await openMenu(me({ role: "admin" }));
    expect(screen.getByRole("menuitem", { name: "Log out everywhere" })).toBeInTheDocument();
  });

  it.each(["viewer", "", "Admin"])("has no Log out everywhere for role %j", async (role) => {
    await openMenu(me({ authMode: "oidc", role }));
    expect(screen.getByRole("menuitem", { name: "Log out" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Log out everywhere" })).toBeNull();
  });

  it.each([
    ["Log out", "this"],
    ["Log out everywhere", "all"],
  ])("%s POSTs scope %s with the CSRF header, then loads /login", async (item, scope) => {
    const stub = stubFetch({ [LOGOUT]: undefined });
    const u = await openMenu(me({ authMode: "oidc", role: "admin" }));
    await u.click(screen.getByRole("menuitem", { name: item }));

    await waitFor(() => expect(assign).toHaveBeenCalledWith("/login"));
    const calls = stub.callsTo(LOGOUT);
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ scope });
    expect(calls[0].headers.get("X-Dash-Csrf")).toBe("1");
  });

  it("loads /login when the session was gone already (401)", async () => {
    stubFetch({
      [LOGOUT]: () => json({ error: "authentication required", code: "unauthenticated" }, 401),
    });
    const u = await openMenu(me());
    await u.click(screen.getByRole("menuitem", { name: "Log out" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/login"));
    expect(toast.error).not.toHaveBeenCalled();
  });

  it.each([
    [500, "Logging out failed. Try again."],
    [403, "Only an administrator can log everyone out."],
  ])("stays and says so when the logout fails with %i", async (status, want) => {
    stubFetch({ [LOGOUT]: () => json({ error: "nope", code: "x" }, status) });
    const u = await openMenu(me());
    await u.click(screen.getByRole("menuitem", { name: "Log out everywhere" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith(want));
    expect(assign).not.toHaveBeenCalled();
  });

  it("stays and says so when dash is unreachable", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch");
      }),
    );
    const u = await openMenu(me());
    await u.click(screen.getByRole("menuitem", { name: "Log out" }));
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("Logging out failed. Try again."));
    expect(assign).not.toHaveBeenCalled();
  });
});
