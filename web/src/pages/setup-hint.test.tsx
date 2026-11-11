import { act, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Me } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { me } from "@/settings/fixtures";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
import { OverviewPage } from "./overview";

// The topology diagram measures and animates; these tests are about the hint above it.
vi.mock("@/pages/topology", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  TopologyPanel: () => <div data-testid="topology" />,
}));

/** A fresh install: every plugin with topics is unconfigured; prometheus has no topics. */
const fresh = () =>
  shimRows().map((p) =>
    p.topics.length > 0 ? { ...p, state: "unconfigured", message: "not configured" } : p,
  );

describe("the Overview's setup hint", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => stream.close());

  /** Renders the Overview as `who` and resolves once /me has been read and drawn. */
  const draw = async (who: Me) => {
    stubFetch({ "GET /api/v1/me": who });
    const { queryClient } = renderApp(<OverviewPage />);
    await waitFor(() => expect(queryClient.getQueryData(["me"])).toBeDefined());
    await act(async () => {});
  };
  const hint = () => screen.queryByRole("region", { name: "Nothing to show yet" });

  it("guides an administrator while no plugin publishes anything", async () => {
    await draw(me("admin"));
    stream.plugins(fresh());
    expect(hint()).not.toBeNull();
    expect(screen.getByRole("link", { name: "Add a host over SSH" })).toHaveAttribute(
      "href",
      "/settings/plugins/ssh",
    );
    expect(screen.getByRole("link", { name: "Open the settings" })).toHaveAttribute(
      "href",
      "/settings",
    );
  });

  it("goes once a plugin with topics is visible, and comes back when it goes", async () => {
    await draw(me("admin"));
    stream.plugins(fresh());
    expect(hint()).not.toBeNull();
    const rows = withState(fresh(), "ssh", "error", "set DASH_SECRET_KEY");
    stream.plugins(rows);
    expect(hint()).toBeNull();
    stream.plugins(withState(rows, "ssh", "disabled"));
    expect(hint()).not.toBeNull();
  });

  it("is not shown with plugins that publish, as in demo mode's populated overview", async () => {
    await draw(me("admin"));
    stream.plugins(shimRows());
    expect(hint()).toBeNull();
  });

  it("is never shown in demo mode, which has no settings", async () => {
    await draw(me("admin", { authMode: "demo" }));
    stream.plugins([pluginInfo({ id: "prometheus" })]);
    expect(hint()).toBeNull();
  });

  it("is never shown to a viewer", async () => {
    await draw(me("viewer"));
    stream.plugins(fresh());
    expect(hint()).toBeNull();
  });
});
