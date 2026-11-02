import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, it } from "vitest";
import { CORE_PAGES, MODULES } from "@/plugins/registry";
import { PluginRoutes } from "@/plugins/routes";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { renderApp, stubFetch } from "@/test/harness";
import { shimRows } from "@/test/shims";
import { Shell } from "./shell";

const WORDING =
  "Authentication is off (auth.mode=none): every visitor is an administrator. Set auth.mode to protect this dashboard.";

// Every route the registry serves, with optional and wildcard segments dropped, plus a deep link,
// a generic plugin page and the not-found page.
const PATHS = [
  ...new Set([
    ...CORE_PAGES.map((p) => p.path),
    ...MODULES.flatMap((m) => (m.routes ?? []).map((r) => r.path)).map((p) =>
      p.replace(/\/\*\?$/, "").replace(/\/:\w+\?$/, ""),
    ),
    "/vms/101",
    "/x/launcher",
    "/no-such-page",
  ]),
];

describe("TestNoneBannerOnEveryPage", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => stream.close());

  it.each(PATHS)("shows the auth.mode=none banner on %s", async (path) => {
    stubFetch({
      "GET /api/v1/me": {
        user: "u",
        role: "admin",
        authMode: "none",
        scopes: ["read"],
        ui: { iconBaseUrl: "" },
        warnings: ["auth_none"],
      },
    });
    renderApp(
      <Shell>
        <PluginRoutes />
      </Shell>,
      { path },
    );
    stream.plugins(shimRows());
    const banner = await screen.findByText(WORDING);
    if (banner.closest('[role="alert"]') === null) throw new Error("banner is not an alert");
  });
});
