import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, type MockInstance, vi } from "vitest";
import { json, renderApp, stubFetch } from "@/test/harness";
import { browserNav } from "./handle401";
import { LoginPage } from "./login-page";

// Route handlers, not Response values: a Response body can be read once, and every test probes /me.
const ME_LOCAL = () =>
  json(
    {
      error: "authentication required",
      code: "unauthenticated",
      details: { mode: "local", setup: false },
    },
    401,
  );
const ME_OIDC = () =>
  json(
    {
      error: "authentication required",
      code: "unauthenticated",
      details: { mode: "oidc", setup: false, oidc: { issuer: "id.example.test" } },
    },
    401,
  );
const BAD_CREDENTIALS = () =>
  json({ error: "invalid credentials", code: "unauthenticated", requestId: "r1" }, 401);
const SECRET = "correct horse battery staple";

let assign: MockInstance<(url: string) => void>;
let reload: MockInstance<() => void>;

beforeEach(() => {
  assign = vi.spyOn(browserNav, "assign").mockImplementation(() => {});
  reload = vi.spyOn(browserNav, "reload").mockImplementation(() => {});
});

async function signIn(user = "admin", password = SECRET) {
  const u = userEvent.setup();
  await u.type(await screen.findByLabelText("Username"), user);
  await u.type(screen.getByLabelText("Password"), password);
  await u.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("LoginPage", () => {
  it("POSTs /login with the CSRF header, drops the cached /me and loads the validated next", async () => {
    const stub = stubFetch({ "GET /api/v1/me": ME_LOCAL, "POST /login": undefined });
    const view = renderApp(<LoginPage />, { path: "/login?next=%2Fpods%3Fns%3Dmedia" });
    const invalidate = vi.spyOn(view.queryClient, "invalidateQueries");
    await signIn();

    await waitFor(() => expect(assign).toHaveBeenCalledWith("/pods?ns=media"));
    const [call] = stub.callsTo("POST /login");
    expect(call.headers.get("X-Dash-Csrf")).toBe("1");
    expect(call.headers.get("Content-Type")).toBe("application/json");
    expect(call.body).toEqual({ username: "admin", password: SECRET });
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["me"] });
    expect(invalidate.mock.invocationCallOrder[0]).toBeLessThan(assign.mock.invocationCallOrder[0]);
    expect(assign).toHaveBeenCalledTimes(1);
    expect(reload).not.toHaveBeenCalled();
  });

  it.each([
    ["no next", "/login", "/"],
    ["a protocol-relative next", "/login?next=%2F%2Fevil.example", "/"],
    ["a backslash next", "/login?next=%2F%5Cevil.example", "/"],
    ["an absolute next", "/login?next=https%3A%2F%2Fevil.example", "/"],
    ["a javascript: next", "/login?next=javascript%3Aalert(1)", "/"],
    ["a next back to /login", "/login?next=%2Flogin", "/"],
    [
      "a next that normalizes to //evil",
      `/login?next=${encodeURIComponent("/.//evil.example/x")}`,
      "/",
    ],
    [
      "an encoded-dot next to //evil",
      `/login?next=${encodeURIComponent("/%2e//evil.example/x")}`,
      "/",
    ],
    [
      "a parent-segment next to //evil",
      `/login?next=${encodeURIComponent("/a/..//evil.example/x")}`,
      "/",
    ],
    ["a dot-segment next to a page", `/login?next=${encodeURIComponent("/./alerts")}`, "/alerts"],
  ])("loads / for %s", async (_name, path, want) => {
    stubFetch({ "GET /api/v1/me": ME_LOCAL, "POST /login": undefined });
    renderApp(<LoginPage />, { path });
    await signIn();
    await waitFor(() => expect(assign).toHaveBeenCalledWith(want));
  });

  it("shows one generic message for refused credentials, clears the password, and stays", async () => {
    stubFetch({ "GET /api/v1/me": ME_LOCAL, "POST /login": BAD_CREDENTIALS });
    renderApp(<LoginPage />, { path: "/login" });
    await signIn("nobody", SECRET);

    expect(await screen.findByRole("alert")).toHaveTextContent("Invalid username or password.");
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(screen.getByLabelText("Username")).toHaveValue("nobody");
    expect(document.body.textContent).not.toContain(SECRET);
    expect(document.body.textContent).not.toContain("invalid credentials");
    expect(assign).not.toHaveBeenCalled();
    expect(reload).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled();
  });

  it.each([
    [500, "internal"],
    [403, "csrf_failed"],
    [503, "upstream_unavailable"],
  ])("shows the fixed text for a %i %s, never the server's words", async (status, code) => {
    stubFetch({
      "GET /api/v1/me": ME_LOCAL,
      "POST /login": () => json({ error: `server says ${code}`, code }, status),
    });
    renderApp(<LoginPage />, { path: "/login" });
    await signIn();
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Signing in failed. Reload the page and try again.",
    );
    expect(document.body.textContent).not.toContain("server says");
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(assign).not.toHaveBeenCalled();
  });

  it.each([
    ["30", "Too many attempts. Try again in 30 seconds."],
    ["1", "Too many attempts. Try again in 1 second."],
    ["60", "Too many attempts. Try again in 1 minute."],
    ["900", "Too many attempts. Try again in 15 minutes."],
    ["61", "Too many attempts. Try again in 2 minutes."],
    ["Wed, 21 Oct 2026 07:28:00 GMT", "Too many attempts. Try again later."],
    ["-5", "Too many attempts. Try again later."],
    [null, "Too many attempts. Try again later."],
  ])("shows a 429 with Retry-After %j", async (retryAfter, want) => {
    const headers: Record<string, string> =
      retryAfter === null ? {} : { "Retry-After": retryAfter };
    stubFetch({
      "GET /api/v1/me": ME_LOCAL,
      "POST /login": () => json({ error: "too many attempts", code: "rate_limited" }, 429, headers),
    });
    renderApp(<LoginPage />, { path: "/login" });
    await signIn();
    expect(await screen.findByRole("alert")).toHaveTextContent(want);
    expect(assign).not.toHaveBeenCalled();
  });

  it("sends one request while one is pending", async () => {
    // The tsconfig lib predates Promise.withResolvers (es2024).
    let answer: (r: Response) => void = () => {};
    const gate = new Promise<Response>((resolve) => {
      answer = resolve;
    });
    const stub = stubFetch({ "GET /api/v1/me": ME_LOCAL, "POST /login": () => gate });
    renderApp(<LoginPage />, { path: "/login" });
    await signIn();
    const button = await screen.findByRole("button", { name: "Signing in…" });
    expect(button).toBeDisabled();
    await userEvent.setup().click(button);
    expect(stub.callsTo("POST /login")).toHaveLength(1);
    answer(BAD_CREDENTIALS());
    expect(await screen.findByRole("alert")).toHaveTextContent("Invalid username or password.");
  });

  it.each([
    ["not_allowed", "Your account is not allowed to sign in to dash."],
    ["oidc_failed", "Signing in with the identity provider failed."],
  ])("explains ?error=%s", async (code, want) => {
    stubFetch({ "GET /api/v1/me": ME_OIDC });
    renderApp(<LoginPage />, { path: `/login?error=${code}` });
    expect(await screen.findByRole("alert")).toHaveTextContent(want);
  });

  it.each(["toString", "__proto__", "<b>x</b>", "constructor"])(
    "ignores an unknown ?error=%s and never echoes it",
    async (code) => {
      stubFetch({ "GET /api/v1/me": ME_LOCAL });
      renderApp(<LoginPage />, { path: `/login?error=${encodeURIComponent(code)}` });
      await screen.findByLabelText("Username");
      expect(screen.queryByRole("alert")).toBeNull();
      expect(document.body.textContent).not.toContain(code);
    },
  );

  it("offers OIDC sign-in when the 401 names an identity provider, carrying the validated next", async () => {
    stubFetch({ "GET /api/v1/me": ME_OIDC });
    renderApp(<LoginPage />, { path: "/login?next=%2Fpods" });
    const link = await screen.findByRole("link", { name: "OIDC sign-in" });
    expect(link).toHaveAttribute("href", "/auth/oidc/start?next=%2Fpods");
    expect(screen.getByText("id.example.test")).toBeInTheDocument();
    // oidc mode has no local account: no password form
    expect(screen.queryByLabelText("Password")).toBeNull();
  });

  it("gives the OIDC button / for an unsafe next", async () => {
    stubFetch({ "GET /api/v1/me": ME_OIDC });
    renderApp(<LoginPage />, { path: "/login?next=%2F%2Fevil.example" });
    const link = await screen.findByRole("link", { name: "OIDC sign-in" });
    expect(link).toHaveAttribute("href", "/auth/oidc/start?next=%2F");
  });

  it("gives the OIDC button and the proxy Continue link the validated next, never a normalized //", async () => {
    const evil = `/login?next=${encodeURIComponent("/.//evil.example/x")}`;
    stubFetch({ "GET /api/v1/me": ME_OIDC });
    const oidc = renderApp(<LoginPage />, { path: evil });
    expect(await screen.findByRole("link", { name: "OIDC sign-in" })).toHaveAttribute(
      "href",
      "/auth/oidc/start?next=%2F",
    );
    oidc.unmount();
    stubFetch({
      "GET /api/v1/me": () =>
        json(
          {
            error: "authentication required",
            code: "unauthenticated",
            details: { mode: "proxy", setup: false },
          },
          401,
        ),
    });
    renderApp(<LoginPage />, { path: evil });
    expect(await screen.findByRole("link", { name: "Continue" })).toHaveAttribute("href", "/");
  });

  it.each([
    ["/login?next=%2Fpods%3Fns%3Da", "/pods?ns=a"],
    [`/login?next=${encodeURIComponent("/.//evil.example/x")}`, "/"],
  ])(
    "gives a signed-in OIDC user on %s a Continue link to the validated next",
    async (path, want) => {
      stubFetch({
        "GET /api/v1/me": () =>
          json({ user: "alice", role: "viewer", authMode: "oidc", scopes: ["read"], warnings: [] }),
      });
      renderApp(<LoginPage />, { path });
      expect(await screen.findByRole("link", { name: "Continue" })).toHaveAttribute("href", want);
      expect(screen.getByText(/You are signed in/)).toBeInTheDocument();
      expect(screen.queryByRole("link", { name: "OIDC sign-in" })).toBeNull();
      expect(screen.queryByLabelText("Password")).toBeNull();
    },
  );

  it("has no OIDC button in local mode", async () => {
    stubFetch({ "GET /api/v1/me": ME_LOCAL });
    renderApp(<LoginPage />, { path: "/login" });
    await screen.findByLabelText("Username");
    await waitFor(() => expect(screen.queryByRole("link", { name: "OIDC sign-in" })).toBeNull());
  });

  it("points to the setup line instead of a form while the local account does not exist", async () => {
    stubFetch({
      "GET /api/v1/me": () =>
        json(
          {
            error: "authentication required",
            code: "unauthenticated",
            details: { mode: "local", setup: true },
          },
          401,
        ),
    });
    renderApp(<LoginPage />, { path: "/login" });
    expect(await screen.findByRole("status")).toHaveTextContent("setup token required");
    expect(screen.queryByLabelText("Password")).toBeNull();
    expect(screen.queryByRole("button", { name: "Sign in" })).toBeNull();
  });

  it("keeps the form when the probe fails, so a broken /me never locks the admin out", async () => {
    stubFetch({ "GET /api/v1/me": () => json({ error: "boom", code: "internal" }, 500) });
    renderApp(<LoginPage />, { path: "/login" });
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
  });
});
