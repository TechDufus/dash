import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { me, settingsRoutes, settingsView } from "@/settings/fixtures";
import { renderApp, stubFetch } from "@/test/harness";
import { Shell } from "./shell";

// The shell mounts the administrator's notices on every page: the restart-required list from
// GET /api/v1/settings (TestRestartRequiredAfterAFileChangeOfAServerKey answers it with
// server.listen) and the warnings /me carries.

let stream: TestStream;
beforeEach(() => {
  stream = connectStream();
});
afterEach(() => stream.close());

describe("the shell's settings notices", () => {
  it("shows the restart list and the admin warnings on any page", async () => {
    stubFetch(
      settingsRoutes({
        "GET /api/v1/me": me("admin", { authMode: "local", warnings: ["anonymous_viewer"] }),
        "GET /api/v1/settings": settingsView({ restartRequired: ["server.listen"] }),
      }),
    );
    renderApp(
      <Shell>
        <p>page</p>
      </Shell>,
    );
    expect(await screen.findByText("server.listen")).toBeInTheDocument();
    expect(screen.getByText(/Restart required/)).toBeInTheDocument();
    const statuses = await screen.findAllByRole("status");
    expect(statuses.some((s) => /anonymous/i.test(s.textContent ?? ""))).toBe(true);
  });
});
