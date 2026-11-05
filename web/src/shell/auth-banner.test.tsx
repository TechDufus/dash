import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Me } from "@/api/types";
import { json, renderApp, stubFetch } from "@/test/harness";
import { AuthBanner } from "./auth-banner";

const WORDING =
  "Authentication is off (auth.mode=none): every visitor is an administrator. Set auth.mode to protect this dashboard.";

const me = (over: Partial<Me> = {}) => ({
  user: "u",
  role: "viewer",
  authMode: "proxy",
  scopes: ["read"],
  audience: "api",
  version: "test",
  capabilities: { actions: false, mcp: false, push: false, oauth: false, tokens: false },
  ui: { iconBaseUrl: "" },
  warnings: [],
  ...over,
});

/** Renders the banner and waits until /me has been read into the query cache. */
async function show(body: unknown) {
  const stub = stubFetch({ "GET /api/v1/me": body });
  const view = renderApp(<AuthBanner />);
  await waitFor(() => expect(view.queryClient.getQueryData(["me"])).toBeDefined());
  expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1);
  return view;
}

describe("AuthBanner", () => {
  it("renders the warning in none mode, as an alert with the fixed wording", async () => {
    await show(me({ authMode: "none", role: "admin" }));
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveTextContent(WORDING);
    expect(banner.textContent).toBe(WORDING);
  });

  it("renders from the auth_none warning alone, whatever the mode says", async () => {
    await show(me({ authMode: "proxy", warnings: ["auth_none"] }));
    expect(await screen.findByRole("alert")).toHaveTextContent(WORDING);
  });

  it("renders when an older server sends no warnings at all, in none mode", async () => {
    const { warnings: _omitted, ...old } = me({ authMode: "none" });
    await show(old);
    expect(await screen.findByRole("alert")).toHaveTextContent(WORDING);
  });

  it("is drawn in the destructive colours", async () => {
    await show(me({ authMode: "none" }));
    const banner = await screen.findByRole("alert");
    expect(banner).toHaveClass("text-destructive");
    expect(banner).toHaveClass("bg-destructive/10");
  });

  it("has no dismiss control: nothing to click, and it stays through a re-render", async () => {
    const view = await show(me({ authMode: "none" }));
    const banner = await screen.findByRole("alert");
    expect(screen.queryAllByRole("button")).toHaveLength(0);
    expect(banner.querySelector("button, a, [role=button]")).toBeNull();
    view.rerender(<AuthBanner />);
    expect(screen.getByRole("alert")).toBe(banner);
  });

  it("renders nothing in proxy mode", async () => {
    await show(me({ authMode: "proxy" }));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("renders nothing for other warnings", async () => {
    await show(me({ authMode: "proxy", warnings: ["admin_wildcard"] }));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("renders nothing for a null warnings list", async () => {
    await show(me({ authMode: "proxy", warnings: null as unknown as string[] }));
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("renders nothing while /me is loading", async () => {
    const stub = stubFetch({ "GET /api/v1/me": () => new Promise(() => {}) });
    const view = renderApp(<AuthBanner />);
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    expect(view.queryClient.getQueryState(["me"])?.status).toBe("pending");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("renders nothing when /me fails", async () => {
    stubFetch({ "GET /api/v1/me": json({ error: "boom" }, 500) });
    const view = renderApp(<AuthBanner />);
    await waitFor(() => expect(view.queryClient.getQueryState(["me"])?.status).toBe("error"));
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
