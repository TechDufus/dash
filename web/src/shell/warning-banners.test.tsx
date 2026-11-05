import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { me } from "@/settings/fixtures";
import { renderApp, stubFetch } from "@/test/harness";
import { WarningBanners } from "./warning-banners";

/** Renders the banners for `body` as /me and waits until it is in the query cache. */
async function show(body: unknown) {
  stubFetch({ "GET /api/v1/me": body });
  const view = renderApp(<WarningBanners />);
  await waitFor(() => expect(view.queryClient.getQueryData(["me"])).toBeDefined());
  return view;
}

const BOTH = ["anonymous_viewer", "oidc_open"];

describe("WarningBanners", () => {
  it("shows an administrator one status notice per admin warning", async () => {
    await show(me("admin", { warnings: BOTH }));
    const notices = await screen.findAllByRole("status");
    expect(notices).toHaveLength(2);
    expect(notices[0]).toHaveTextContent("auth.local.anonymousViewer");
    expect(notices[1]).toHaveTextContent("auth.oidc.allowedUsers");
  });

  it("shows a viewer none of them", async () => {
    await show(me("viewer", { warnings: BOTH }));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("ignores the warnings other banners own", async () => {
    await show(me("admin", { warnings: ["admin_wildcard", "auth_none", "toString"] }));
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("keeps a dismissed notice hidden for the rest of the session, and only that one", async () => {
    const user = userEvent.setup();
    const first = await show(me("admin", { warnings: BOTH }));
    const [anon] = await screen.findAllByRole("status");
    if (!anon) throw new Error("no notice");
    await user.click(within(anon).getByRole("button", { name: "Dismiss" }));
    expect(screen.getAllByRole("status")).toHaveLength(1);
    expect(screen.getByRole("status")).toHaveTextContent("auth.oidc.allowedUsers");

    // A new page load in the same session: a fresh tree and query cache, the same sessionStorage.
    first.unmount();
    await show(me("admin", { warnings: BOTH }));
    const again = await screen.findAllByRole("status");
    expect(again).toHaveLength(1);
    expect(again[0]).toHaveTextContent("auth.oidc.allowedUsers");
  });

  it("shows a dismissed notice again in a new session", async () => {
    const user = userEvent.setup();
    const first = await show(me("admin", { warnings: ["oidc_open"] }));
    await user.click(await screen.findByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("status")).toBeNull();
    first.unmount();

    sessionStorage.clear();
    await show(me("admin", { warnings: ["oidc_open"] }));
    expect(await screen.findByRole("status")).toHaveTextContent("auth.oidc.allowedUsers");
  });
});
