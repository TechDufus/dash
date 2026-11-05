import { focusManager } from "@tanstack/react-query";
import { act, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { settingsPaths } from "@/settings/api";
import { me, settingsRoutes, settingsView } from "@/settings/fixtures";
import { renderApp, stubFetch } from "@/test/harness";
import { RestartBanner } from "./restart-banner";

const VIEW = `GET ${settingsPaths.view}`;

afterEach(() => {
  focusManager.setFocused(undefined);
});

describe("RestartBanner", () => {
  it("lists the restart-required paths of GET /settings", async () => {
    // The body TestRestartRequiredAfterAFileChangeOfAServerKey asserts after a file change.
    stubFetch(
      settingsRoutes({
        [VIEW]: settingsView({ restartRequired: ["server.listen", "ui.iconBaseURL"] }),
      }),
    );
    renderApp(<RestartBanner />);
    const banner = await screen.findByRole("status");
    expect(banner).toHaveTextContent("Restart required");
    expect(Array.from(banner.querySelectorAll("li"), (li) => li.textContent)).toEqual([
      "server.listen",
      "ui.iconBaseURL",
    ]);
  });

  it("draws nothing when no restart is required", async () => {
    const stub = stubFetch(settingsRoutes());
    renderApp(<RestartBanner />);
    await waitFor(() => expect(stub.callsTo(VIEW)).toHaveLength(1));
    await waitFor(() => expect(screen.queryByRole("status")).toBeNull());
  });

  it("draws nothing for a viewer and never reads the settings", async () => {
    const stub = stubFetch(
      settingsRoutes({
        "GET /api/v1/me": me("viewer"),
        [VIEW]: settingsView({ restartRequired: ["server.listen"] }),
      }),
    );
    const view = renderApp(<RestartBanner />);
    await waitFor(() => expect(view.queryClient.getQueryData(["me"])).toBeDefined());
    expect(stub.callsTo(VIEW)).toHaveLength(0);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("re-reads the list every 30 s: a restart clears the notice", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    const stub = stubFetch(
      settingsRoutes({ [VIEW]: settingsView({ restartRequired: ["server.listen"] }) }),
    );
    renderApp(<RestartBanner />);
    await screen.findByRole("status");

    // dash restarted: the new process has nothing pending.
    stub.route(VIEW, settingsView({ restartRequired: [] }));
    act(() => {
      vi.advanceTimersByTime(29_999);
    });
    expect(stub.callsTo(VIEW)).toHaveLength(1);
    act(() => {
      vi.advanceTimersByTime(1);
    });
    await waitFor(() => expect(screen.queryByRole("status")).toBeNull());
    expect(stub.callsTo(VIEW)).toHaveLength(2);
  });

  it("re-reads the list when the window regains focus", async () => {
    const stub = stubFetch(settingsRoutes());
    renderApp(<RestartBanner />);
    await waitFor(() => expect(stub.callsTo(VIEW)).toHaveLength(1));
    expect(screen.queryByRole("status")).toBeNull();

    stub.route(VIEW, settingsView({ restartRequired: ["server.listen"] }));
    act(() => {
      focusManager.setFocused(false);
      focusManager.setFocused(true);
    });
    expect(await screen.findByRole("status")).toHaveTextContent("server.listen");
  });
});
