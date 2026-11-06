import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AttentionItem } from "@/api/types";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { me } from "@/settings/fixtures";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
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
  ref: `${plugin || "core"}:Item/${id}`,
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

describe("AttentionPanel Copy context for AI", () => {
  afterEach(() => {
    Reflect.deleteProperty(navigator, "clipboard");
  });

  it("copies the context of each item whose plugin implements Objects", async () => {
    const writeText = vi.fn(async (_: string) => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const f = stubFetch({
      "GET /api/v1/me": me("viewer", { scopes: ["read"] }),
      "GET /api/v1/plugins": [
        pluginInfo({ id: "kubernetes", objects: true }),
        pluginInfo({ id: "flux", objects: false }),
      ],
      "GET /api/v1/context": () => new Response("# dash context bundle\n"),
    });
    const r = renderApp(<AttentionPanel items={items.slice(0, 3)} />);
    await waitFor(() => expect(r.queryClient.getQueryData(["me"])).toBeDefined());
    await waitFor(() => expect(r.queryClient.getQueryData(["plugins"])).toBeDefined());
    // The flux item gets no button: its ref would answer 404 "unknown object".
    const buttons = await screen.findAllByRole("button", { name: "Copy context for AI" });
    expect(buttons).toHaveLength(2);

    fireEvent.click(buttons[1]);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("# dash context bundle\n"));
    expect(f.callsTo("GET /api/v1/context").map((c) => c.query.get("ref"))).toEqual([
      "kubernetes:Item/pvc",
    ]);
  });
});
