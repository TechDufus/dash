import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { Endpoint, StatusData } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp } from "@/test/harness";
import { StatusPage } from "./status";

// The test builds the plugin row itself: the page needs only the gatus row that lists its topic,
// not the shim rows the shared fixtures hold.
const gatus = pluginInfo({
  id: "gatus",
  name: "Gatus",
  topics: ["gatus.status"],
  nav: [{ path: "/status", label: "Status", title: "Service status", icon: "Activity" }],
});

const AT = "2026-01-01T00:00:00Z";

const endpoint = (group: string, name: string, healthy = true): Endpoint => ({
  ref: `gatus:Endpoint/${group}_${name}`,
  group,
  name,
  key: `${group}_${name}`,
  url: `${name}.example.com`,
  healthy,
  results: [{ success: healthy, timestamp: AT, durationMs: 10 }],
});

const data = (criticalGroups: string[] | undefined, ...eps: Endpoint[]): StatusData => ({
  endpoints: eps,
  ...(criticalGroups ? { criticalGroups } : {}),
});

/** The group headings, top to bottom. */
const groupTitles = () =>
  screen.getAllByRole("heading").map((h) => h.textContent?.replace(/\d+\/\d+ healthy$/, "").trim());

describe("Status page", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => stream.close());

  function show(d: StatusData) {
    renderApp(<StatusPage />);
    stream.plugins([gatus]);
    stream.send("gatus.status", d, [{ name: "gatus", ok: true, checkedAt: AT }]);
  }

  const eps = [
    endpoint("platform", "grafana"),
    endpoint("infrastructure", "unifi", false),
    endpoint("apps", "plex"),
    endpoint("Edge", "site"),
    endpoint("infra-lab", "lab"),
  ];

  it("lists the critical groups first, then the rest alphabetically", () => {
    show(data(["infrastructure", "edge"], ...eps));
    expect(groupTitles()).toEqual(["Edge", "infrastructure", "apps", "infra-lab", "platform"]);
  });

  it("matches the critical groups without regard to case, on either side", () => {
    show(data(["INFRASTRUCTURE", "edge"], ...eps));
    expect(groupTitles()).toEqual(["Edge", "infrastructure", "apps", "infra-lab", "platform"]);
  });

  it("orders alphabetically when no group is critical (no 'infra' guess)", () => {
    show(data(undefined, ...eps));
    expect(groupTitles()).toEqual(["apps", "Edge", "infra-lab", "infrastructure", "platform"]);
  });

  it("does the same with an empty list", () => {
    show(data([], ...eps));
    expect(groupTitles()).toEqual(["apps", "Edge", "infra-lab", "infrastructure", "platform"]);
  });

  it("puts the failing checks of a group first and counts what is healthy", () => {
    show(data([], endpoint("apps", "b"), endpoint("apps", "a", false), endpoint("apps", "c")));
    const rows = screen.getAllByText(/^[abc]$/).map((r) => r.textContent);
    expect(rows).toEqual(["a", "b", "c"]);
    expect(screen.getByText(/2\/3 healthy/)).toBeInTheDocument();
  });

  it("shows the empty state for no endpoints", () => {
    show(data([]));
    expect(screen.getByText("No endpoints reported.")).toBeInTheDocument();
  });
});
