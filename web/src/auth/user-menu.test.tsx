import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, type Mock, type MockInstance, vi } from "vitest";
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

  it("offers Log out everywhere to every user, and Sign out all users to an administrator", async () => {
    await openMenu(me({ role: "admin" }));
    expect(screen.getByRole("menuitem", { name: "Log out everywhere" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Sign out all users" })).toBeInTheDocument();
  });

  it("says a logout stops push on all the user's devices where push is available", async () => {
    const push = { actions: false, mcp: false, push: true, oauth: false, tokens: false };
    await openMenu(me({ role: "admin", capabilities: push }));
    for (const name of ["Log out", "Log out everywhere"]) {
      expect(screen.getByRole("menuitem", { name })).toHaveAccessibleDescription(
        "Logging out stops push notifications on all your devices.",
      );
    }
    expect(
      screen.getByRole("menuitem", { name: "Sign out all users" }),
    ).toHaveAccessibleDescription("Stops push notifications on every user's devices.");
  });

  it("has no push help line where push is not available", async () => {
    await openMenu(me({ role: "admin" }));
    expect(screen.queryByText(/stops push notifications/i)).toBeNull();
    expect(screen.getByRole("menuitem", { name: "Log out" })).not.toHaveAccessibleDescription();
  });

  it.each(["viewer", "", "Admin"])("has no Sign out all users for role %j", async (role) => {
    await openMenu(me({ authMode: "oidc", role }));
    expect(screen.getByRole("menuitem", { name: "Log out" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Log out everywhere" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: "Sign out all users" })).toBeNull();
  });

  it("Log out POSTs scope this with the CSRF header, then loads /login", async () => {
    const stub = stubFetch({ [LOGOUT]: undefined });
    const u = await openMenu(me({ authMode: "oidc", role: "admin" }));
    await u.click(screen.getByRole("menuitem", { name: "Log out" }));

    await waitFor(() => expect(assign).toHaveBeenCalledWith("/login"));
    const calls = stub.callsTo(LOGOUT);
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ scope: "this" });
    expect(calls[0].headers.get("X-Dash-Csrf")).toBe("1");
  });

  it.each([
    ["Log out everywhere", "user", /every app you connected is disconnected/],
    ["Sign out all users", "all", /every session of every user ends.*every connected app/i],
  ])("%s asks first, then POSTs scope %s and loads /login", async (item, scope, says) => {
    const stub = stubFetch({ [LOGOUT]: undefined });
    const u = await openMenu(me({ authMode: "oidc", role: "admin" }));
    await u.click(screen.getByRole("menuitem", { name: item }));

    const confirm = await screen.findByRole("alertdialog");
    expect(confirm).toHaveTextContent(says);
    expect(confirm).toHaveTextContent(/personal access tokens stay/i);
    expect(stub.callsTo(LOGOUT)).toHaveLength(0);

    await u.click(within(confirm).getByRole("button", { name: item }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/login"));
    const calls = stub.callsTo(LOGOUT);
    expect(calls).toHaveLength(1);
    expect(calls[0].body).toEqual({ scope });
    expect(calls[0].headers.get("X-Dash-Csrf")).toBe("1");
  });

  it.each(["Log out everywhere", "Sign out all users"])(
    "sends nothing when %s is cancelled",
    async (item) => {
      const stub = stubFetch({ [LOGOUT]: undefined });
      const u = await openMenu(me({ role: "admin" }));
      await u.click(screen.getByRole("menuitem", { name: item }));
      await u.click(
        within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel" }),
      );
      await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
      expect(stub.callsTo(LOGOUT)).toHaveLength(0);
      expect(assign).not.toHaveBeenCalled();
    },
  );

  describe("this browser's push subscription", () => {
    const ID = "sub_ABCDEFGHIJKLMNOPQRSTUVWXYZ";
    const DELETE = `DELETE /api/v1/push/subscriptions/${ID}`;
    const LIST = "GET /api/v1/push/subscriptions";
    let unsubscribe: Mock<() => Promise<boolean>>;

    beforeEach(() => {
      unsubscribe = vi.fn(async () => true);
      const pushManager = { getSubscription: vi.fn(async () => ({ unsubscribe })) };
      Object.defineProperty(navigator, "serviceWorker", {
        value: { getRegistration: vi.fn(async () => ({ pushManager })) },
        configurable: true,
      });
      vi.stubGlobal("isSecureContext", true);
      vi.stubGlobal("PushManager", class {});
      vi.stubGlobal("Notification", {});
      return () => {
        Reflect.deleteProperty(navigator, "serviceWorker");
        localStorage.clear();
      };
    });

    it("is removed and dropped before the logout when it is this user's, keeping its settings", async () => {
      localStorage.setItem("dash.push.subscription:alice", ID);
      const stub = stubFetch({
        [LIST]: {
          subscriptions: [
            {
              id: ID,
              user: "alice",
              deviceName: "Laptop",
              notify: { approvals: false, attention: "warn" },
            },
          ],
        },
        [LOGOUT]: undefined,
        [DELETE]: undefined,
      });
      const u = await openMenu(me({ authMode: "oidc" }));
      await u.click(screen.getByRole("menuitem", { name: "Log out" }));

      await waitFor(() => expect(assign).toHaveBeenCalledWith("/login"));
      expect(unsubscribe).toHaveBeenCalledTimes(1);
      expect(localStorage.getItem("dash.push.subscription:alice")).toBeNull();
      // Its name and notify settings stay for alice's next sign-in here; no ID, no endpoint.
      expect(JSON.parse(localStorage.getItem("dash.push.resubscribe:alice") ?? "null")).toEqual({
        deviceName: "Laptop",
        notify: { approvals: false, attention: "warn" },
      });
      // The list and the DELETE go first, while the session still holds.
      expect(stub.calls.map((c) => `${c.method} ${c.path}`)).toEqual([LIST, DELETE, LOGOUT]);
    });

    it("is left alone when it is another user's", async () => {
      localStorage.setItem("dash.push.subscription:bob", ID);
      const stub = stubFetch({ [LOGOUT]: undefined, [DELETE]: undefined });
      const u = await openMenu(me({ authMode: "oidc" }));
      await u.click(screen.getByRole("menuitem", { name: "Log out" }));

      await waitFor(() => expect(assign).toHaveBeenCalledWith("/login"));
      expect(unsubscribe).not.toHaveBeenCalled();
      expect(stub.callsTo(DELETE)).toEqual([]);
      expect(localStorage.getItem("dash.push.subscription:bob")).toBe(ID);
    });

    it("does not stop the logout when removing it fails", async () => {
      localStorage.setItem("dash.push.subscription:alice", ID);
      const stub = stubFetch({
        [LOGOUT]: undefined,
        [DELETE]: () => json({ error: "boom", code: "internal" }, 500),
      });
      const u = await openMenu(me({ authMode: "oidc" }));
      await u.click(screen.getByRole("menuitem", { name: "Log out" }));

      await waitFor(() => expect(assign).toHaveBeenCalledWith("/login"));
      expect(stub.callsTo(LOGOUT)).toHaveLength(1);
      expect(unsubscribe).toHaveBeenCalledTimes(1);
    });
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
    [403, "Only an administrator can sign out all users."],
  ])("stays and says so when the logout fails with %i", async (status, want) => {
    stubFetch({ [LOGOUT]: () => json({ error: "nope", code: "x" }, status) });
    const u = await openMenu(me());
    await u.click(screen.getByRole("menuitem", { name: "Sign out all users" }));
    await u.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", {
        name: "Sign out all users",
      }),
    );
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
