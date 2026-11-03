import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, type MockInstance, vi } from "vitest";
import { type FetchStub, json, renderApp, stubFetch } from "@/test/harness";
import { browserNav } from "./handle401";
import { SetupPage } from "./setup-page";

const TOKEN = "dash_setup_q5Zk3m0D1yWb8c4tVfN2uE7xLpR9sJhA6oGiKwTnY0Q";
const GOOD = "a long enough password";

let assign: MockInstance<(url: string) => void>;

beforeEach(() => {
  assign = vi.spyOn(browserNav, "assign").mockImplementation(() => {});
});

afterEach(() => {
  window.history.replaceState(null, "", "/");
});

/** Opens /setup with `fragment` as the browser would after following dash's setup line. */
function open(fragment: string, { strict = false } = {}) {
  window.history.replaceState(null, "", `/setup${fragment}`);
  return renderApp(<SetupPage />, { path: "/setup", strict });
}

/** Everything the page could have leaked the token into. */
function expectTokenNowhere(stub?: FetchStub) {
  expect(window.location.href).not.toContain(TOKEN);
  expect(window.location.hash).toBe("");
  expect(JSON.stringify(window.history.state)).not.toContain(TOKEN);
  expect(document.body.innerHTML).not.toContain(TOKEN);
  expect(JSON.stringify({ ...sessionStorage })).not.toContain(TOKEN);
  expect(JSON.stringify({ ...localStorage })).not.toContain(TOKEN);
  for (const url of assign.mock.calls.map((c) => c[0])) expect(url).not.toContain(TOKEN);
  for (const c of stub?.calls ?? []) {
    expect(c.path + c.query.toString()).not.toContain(TOKEN);
  }
}

async function fill(password: string, confirm = password) {
  const u = userEvent.setup();
  await u.type(await screen.findByLabelText("New password"), password);
  await u.type(screen.getByLabelText("Repeat the password"), confirm);
  await u.click(screen.getByRole("button", { name: "Set password and sign in" }));
}

/** The refused password is in no input value and nowhere in the page's HTML. */
function expectPasswordNowhere() {
  expect(document.body.innerHTML).not.toContain(GOOD);
  expect(screen.getByLabelText("New password")).toHaveValue("");
  expect(screen.getByLabelText("Repeat the password")).toHaveValue("");
}

describe("SetupPage", () => {
  it("reads the token from the fragment and strips it from the URL before the form shows", async () => {
    open(`#token=${TOKEN}`);
    expect(await screen.findByLabelText("New password")).toBeInTheDocument();
    expect(window.location.pathname).toBe("/setup");
    expectTokenNowhere();
  });

  it("keeps the query while it strips the fragment", async () => {
    open(`?x=1#token=${TOKEN}`);
    await screen.findByLabelText("New password");
    expect(window.location.search).toBe("?x=1");
    expectTokenNowhere();
  });

  it("POSTs the token and the password with the CSRF header, then loads the app; the token never reaches a URL", async () => {
    const stub = stubFetch({ "POST /setup": undefined });
    const view = open(`#token=${TOKEN}`);
    const invalidate = vi.spyOn(view.queryClient, "invalidateQueries");
    await fill(GOOD);

    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"));
    const [call] = stub.callsTo("POST /setup");
    expect(call.body).toEqual({ token: TOKEN, password: GOOD });
    expect(call.headers.get("X-Dash-Csrf")).toBe("1");
    expect(stub.calls).toHaveLength(1);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ["me"] });
    expectTokenNowhere(stub);
  });

  // StrictMode outermost, as main.tsx renders it: React then runs every effect twice (inside the
  // providers it would not), so a page that read the token in an effect would lose it.
  it("keeps the token through StrictMode's second effect run", async () => {
    const stub = stubFetch({ "POST /setup": undefined });
    open(`#token=${TOKEN}`, { strict: true });
    await fill(GOOD);
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"));
    expect(stub.callsTo("POST /setup")[0].body).toEqual({ token: TOKEN, password: GOOD });
    expectTokenNowhere(stub);
  });

  it.each([
    ["no fragment", ""],
    ["a fragment without a token", "#foo=bar"],
    ["an empty token", "#token="],
    ["a token over 512 characters", `#token=${"a".repeat(513)}`],
  ])("explains the missing link for %s, with no form", async (_name, fragment) => {
    const stub = stubFetch();
    open(fragment);
    expect(await screen.findByRole("alert")).toHaveTextContent("needs the setup link");
    expect(screen.queryByLabelText("New password")).toBeNull();
    expect(window.location.hash).toBe("");
    expect(stub.calls).toHaveLength(0);
  });

  it.each([
    ["11 bytes", "a".repeat(11), "Use at least 12 bytes."],
    ["5 two-byte characters (10 bytes)", "é".repeat(5), "Use at least 12 bytes."],
    ["257 bytes", "a".repeat(257), "Use at most 256 bytes."],
    ["129 two-byte characters (258 bytes)", "é".repeat(129), "Use at most 256 bytes."],
  ])("refuses %s before sending anything", async (_name, password, want) => {
    const stub = stubFetch({ "POST /setup": undefined });
    open(`#token=${TOKEN}`);
    await fill(password);
    expect(await screen.findByRole("alert")).toHaveTextContent(want);
    expect(stub.calls).toHaveLength(0);
    expect(assign).not.toHaveBeenCalled();
  });

  it.each([
    ["12 bytes", "a".repeat(12)],
    ["6 two-byte characters (12 bytes)", "é".repeat(6)],
    ["256 bytes", "a".repeat(256)],
  ])("accepts %s", async (_name, password) => {
    const stub = stubFetch({ "POST /setup": undefined });
    open(`#token=${TOKEN}`);
    await fill(password);
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/"));
    expect(stub.callsTo("POST /setup")[0].body).toEqual({ token: TOKEN, password });
  });

  it("refuses two different passwords before sending anything", async () => {
    const stub = stubFetch({ "POST /setup": undefined });
    open(`#token=${TOKEN}`);
    await fill(GOOD, `${GOOD}!`);
    expect(await screen.findByRole("alert")).toHaveTextContent("The two passwords differ.");
    expect(stub.calls).toHaveLength(0);
  });

  it.each([
    [401, "invalid_token", {}, "This setup link is not valid"],
    [400, "invalid_input", {}, "The password must be 12 to 256 bytes long."],
    [404, "not_found", {}, "The administrator account exists already."],
    [429, "rate_limited", { "Retry-After": "8" }, "Too many attempts. Try again in 8 seconds."],
    [500, "internal", {}, "Setup failed."],
  ])("shows a %i %s without the server's words and stays", async (status, code, headers, want) => {
    const stub = stubFetch({
      "POST /setup": () => json({ error: `server says ${code}`, code }, status, headers),
    });
    open(`#token=${TOKEN}`);
    await fill(GOOD);
    expect(await screen.findByRole("alert")).toHaveTextContent(want);
    expect(document.body.textContent).not.toContain("server says");
    expect(assign).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Set password and sign in" })).toBeEnabled();
    expectTokenNowhere(stub);
    expectPasswordNowhere();
  });

  it("leaves no password in the page after a network error", async () => {
    stubFetch({
      "POST /setup": () => {
        throw new TypeError("Failed to fetch");
      },
    });
    open(`#token=${TOKEN}`);
    await fill(GOOD);
    await screen.findByRole("alert");
    expectPasswordNowhere();
  });
});
