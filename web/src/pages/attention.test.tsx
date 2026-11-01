import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { AttentionItem } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp } from "@/test/harness";
import { shimRows, withState } from "@/test/shims";
import { AttentionPanel } from "./attention";

const item = (id: string, plugin: string, route: string): AttentionItem => ({
  id,
  severity: "warn",
  source: "test",
  plugin,
  title: id,
  detail: "",
  since: "2026-01-01T00:00:00Z",
  route,
  links: [],
});

const queue = pluginInfo({
  id: "queue",
  name: "Queue",
  generic: [{ id: "v", title: "V", kind: "stat", topic: "queue.stats", path: "", columns: [] }],
});

const items = [
  item("pod", "kubernetes", "/cluster/pods/acme/web-0"),
  item("pvc", "kubernetes", "/storage"),
  item("flux", "flux", "/gitops"),
  item("elsewhere", "flux", "/cluster"),
  item("core", "", "/apps"),
  item("ghost", "nomad", "/nomad"),
  item("generic", "queue", "/x/queue"),
  item("generic-wrong", "queue", "/x/other"),
];

/** The item rows whose title is a link, by title. */
const linked = () =>
  items.map((i) => i.id).filter((id) => screen.getByText(id).closest("a") !== null);

describe("AttentionPanel routes", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => stream.close());

  it("links an item only when its plugin is visible and the SPA has a page for the route", () => {
    renderApp(<AttentionPanel items={items} />);
    stream.plugins([...shimRows(), queue]);
    expect(linked()).toEqual(["pod", "pvc", "flux", "core", "generic"]);
    expect(screen.getByText("pod").closest("a")).toHaveAttribute(
      "href",
      "/cluster/pods/acme/web-0",
    );
  });

  it("shows the title as plain text once the plugin that raised it is switched off", () => {
    renderApp(<AttentionPanel items={items} />);
    const rows = [...shimRows(), queue];
    stream.plugins(rows);

    stream.plugins(withState(rows, "flux", "disabled"));
    expect(linked()).toEqual(["pod", "pvc", "core", "generic"]);
    expect(screen.getByText("flux")).toBeInTheDocument();

    stream.plugins(withState(withState(rows, "kubernetes", "unavailable"), "queue", "disabled"));
    expect(linked()).toEqual(["flux", "core"]);
  });
});
