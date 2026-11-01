import { screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GenericColumn, GenericView, PluginInfo } from "@/api/types";
import { pluginInfo, renderApp } from "@/test/harness";
import { PluginRoutes } from "../routes";
import { useNavEntries } from "../state";
import { connectStream, type TestStream } from "../test-util";
import { GenericPluginPage } from "./generic";

const view = (v: Partial<GenericView> & Pick<GenericView, "id" | "kind">): GenericView => ({
  title: v.id,
  topic: "queue.stats",
  path: "",
  columns: [],
  ...v,
});

const jobs = view({
  id: "jobs",
  title: "Jobs",
  kind: "table",
  path: "/jobs",
  columns: [
    { key: "name", label: "Name" },
    { key: "size", label: "Size", format: "bytes" },
    { key: "fill", label: "Fill", format: "percent" },
    { key: "count", label: "Count", format: "number" },
    { key: "seen", label: "Seen", format: "time" },
    { key: "state", label: "State", format: "health" },
  ],
});

const queue: PluginInfo = pluginInfo({
  id: "queue",
  name: "Job queue",
  topics: ["queue.stats"],
  nav: [{ path: "/x/queue", label: "Job queue", title: "Job queue", icon: "Puzzle" }],
  generic: [
    jobs,
    view({
      id: "depth",
      title: "Queue depth",
      kind: "stat",
      path: "/depth",
      label: "messages waiting",
      unit: "msg",
      format: "number",
    }),
    view({
      id: "recent",
      title: "Recent",
      kind: "list",
      path: "/recent",
      columns: [
        { key: "title", label: "Title" },
        { key: "took", label: "Took" },
      ],
    }),
  ],
});

const twoHoursAgo = () => new Date(Date.now() - 2 * 3600_000).toISOString();

const stats = () => ({
  jobs: [
    { name: "resize", size: 1536, fill: 0.5, count: 1234, seen: twoHoursAgo(), state: "ok" },
    { name: "email", size: 3 * 1024 ** 2, fill: 0.97, count: 7, seen: null, state: "crit" },
  ],
  depth: 42,
  recent: [{ title: "nightly backup", took: "12s" }],
});

function NavPaths() {
  const { entries } = useNavEntries();
  return <p>{entries.map((e) => e.path).join(" ")}</p>;
}

describe("generic views", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => stream.close());

  it("draws a table view from a topic envelope, one formatted cell per column", () => {
    renderApp(<GenericPluginPage id="queue" />);
    stream.plugins([queue]);
    stream.send("queue.stats", stats());

    expect(screen.getByRole("heading", { name: "Job queue" })).toBeInTheDocument();
    const table = screen.getByRole("table");
    expect(
      within(table)
        .getAllByRole("columnheader")
        .map((h) => h.textContent),
    ).toEqual(["Name", "Size", "Fill", "Count", "Seen", "State"]);
    const [resize, email] = within(table).getAllByRole("row").slice(1);
    const cells = (row: HTMLElement | undefined) =>
      within(row as HTMLElement)
        .getAllByRole("cell")
        .map((c) => c.textContent);
    expect(cells(resize)).toEqual(["resize", "1.5 KiB", "50%", "1,234", "2h 0m ago", ""]);
    expect(within(resize as HTMLElement).getByRole("img", { name: "OK" })).toBeInTheDocument();
    expect(cells(email)).toEqual(["email", "3.0 MiB", "97%", "7", "–", ""]);
    expect(within(email as HTMLElement).getByRole("img", { name: "Critical" })).toBeInTheDocument();
  });

  it("draws a stat view with its label and unit", () => {
    renderApp(<GenericPluginPage id="queue" />);
    stream.plugins([queue]);
    stream.send("queue.stats", stats());

    const panel = screen.getByRole("heading", { name: "Queue depth" }).closest("[data-slot=panel]");
    expect(panel).toHaveTextContent("42");
    expect(panel).toHaveTextContent("msg");
    expect(panel).toHaveTextContent("messages waiting");
  });

  it("draws a list view with the first column as the primary text", () => {
    renderApp(<GenericPluginPage id="queue" />);
    stream.plugins([queue]);
    stream.send("queue.stats", stats());

    const item = screen.getByRole("listitem");
    expect(item).toHaveTextContent("nightly backup");
    expect(screen.getByTitle("Took")).toHaveTextContent("12s");
  });

  it("shows No data for a bad pointer, a wrong shape or an empty list, and never throws", () => {
    const bad: PluginInfo = {
      ...queue,
      generic: [
        view({
          id: "missing",
          title: "Missing",
          kind: "table",
          path: "/nope",
          columns: jobs.columns,
        }),
        view({ id: "malformed", title: "Malformed", kind: "stat", path: "jobs", columns: [] }),
        view({ id: "shape", title: "Shape", kind: "table", path: "/depth", columns: jobs.columns }),
        view({ id: "mixed", title: "Mixed", kind: "list", path: "/mixed", columns: jobs.columns }),
        view({ id: "empty", title: "Empty", kind: "table", path: "/none", columns: jobs.columns }),
        view({ id: "object", title: "Object", kind: "stat", path: "/recent/0", columns: [] }),
        view({ id: "kind", title: "Odd kind", kind: "gauge", path: "/depth" }),
      ],
    };
    renderApp(<GenericPluginPage id="queue" />);
    stream.plugins([bad]);
    stream.send("queue.stats", { ...stats(), mixed: [1, "two"], none: [] });

    for (const title of ["Missing", "Malformed", "Shape", "Mixed", "Empty", "Object", "Odd kind"]) {
      const panel = screen.getByRole("heading", { name: title }).closest("[data-slot=panel]");
      expect(panel, title).toHaveTextContent("No data");
    }
  });

  it("shows a placeholder until the topic arrives, then the source chips of its envelope", () => {
    renderApp(<GenericPluginPage id="queue" />);
    stream.plugins([queue]);
    expect(document.querySelectorAll("[aria-busy=true]").length).toBeGreaterThan(0);

    stream.send("queue.stats", stats(), [
      { name: "queue-api", ok: false, error: "timeout", checkedAt: "2026-01-01T00:00:00Z" },
    ]);
    expect(document.querySelector("[aria-busy=true]")).toBeNull();
    expect(screen.getAllByText("source timed out: queue-api").length).toBeGreaterThan(0);
  });

  it("renders the disabled page for a plugin that is switched off or unknown", () => {
    renderApp(<GenericPluginPage id="queue" />);
    stream.plugins([{ ...queue, state: "disabled" }]);
    expect(screen.getByRole("status")).toHaveTextContent(/switched off/);
    expect(screen.queryByRole("table")).toBeNull();

    stream.plugins([]);
    expect(screen.getByRole("status")).toHaveTextContent(/no queue plugin/i);
  });

  it("shows the nav entry and the page while the plugin is on, and drops both when it is not, without a reload", () => {
    renderApp(
      <>
        <NavPaths />
        <GenericPluginPage id="queue" />
      </>,
    );
    stream.plugins([queue]);
    stream.send("queue.stats", stats());
    expect(screen.getByText(/\/x\/queue/)).toBeInTheDocument();
    expect(screen.getByRole("table")).toBeInTheDocument();

    stream.plugins([{ ...queue, state: "disabled" }]);
    expect(screen.queryByText(/\/x\/queue/)).toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.getByRole("status")).toHaveTextContent(/switched off/);

    // Enabled again: the entry returns at once; the table when the topic's first value arrives.
    stream.plugins([queue]);
    expect(screen.getByText(/\/x\/queue/)).toBeInTheDocument();
    stream.send("queue.stats", stats());
    expect(screen.getByRole("table")).toBeInTheDocument();
  });

  it("does not list a /x/<id> entry for a plugin that declares no views", () => {
    renderApp(<NavPaths />);
    stream.plugins([{ ...queue, generic: [] }]);
    expect(screen.queryByText(/\/x\/queue/)).toBeNull();
  });

  it("is served at /x/<id> by the app's routes", () => {
    renderApp(<PluginRoutes />, { path: "/x/queue" });
    stream.plugins([queue]);
    stream.send("queue.stats", stats());
    expect(screen.getByRole("heading", { name: "Job queue" })).toBeInTheDocument();
    expect(screen.getByRole("table")).toBeInTheDocument();
  });

  it("renders NotFound, not the disabled page, for a visible plugin without views", () => {
    renderApp(<PluginRoutes />, { path: "/x/queue" });
    stream.plugins([{ ...queue, generic: [] }]);
    expect(screen.getByText("Nothing here. Pick a page from the rail.")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
    expect(screen.queryByText(/is healthy/)).toBeNull();
  });

  it("adds no second h1 and no aria-label on a plain div", () => {
    renderApp(<GenericPluginPage id="queue" />);
    stream.plugins([queue]);
    stream.send("queue.stats", stats());
    expect(screen.queryAllByRole("heading", { level: 1 })).toHaveLength(0);
    expect(screen.getByRole("heading", { level: 2, name: "Job queue" })).toBeInTheDocument();
    expect(document.querySelector("div[aria-label]")).toBeNull();
  });

  it("isolates a view that throws: a muted row replaces it and its siblings stay", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    // columns: null is what a view without columns used to send.
    const broken = view({ id: "broken", title: "Broken", kind: "table", path: "/jobs" });
    broken.columns = null as unknown as GenericColumn[];
    const page: PluginInfo = { ...queue, generic: [broken, queue.generic[1] as GenericView] };
    renderApp(<GenericPluginPage id="queue" />);
    stream.plugins([page]);
    stream.send("queue.stats", stats());
    expect(screen.getByTestId("panel-failed")).toHaveTextContent("This panel failed to render.");
    expect(screen.getByRole("heading", { name: "Queue depth" })).toBeInTheDocument();
  });

  it("fails alone when the page itself throws: the route shows the reload prompt", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    // A null view in the list: the page reads its id while mapping, outside any panel.
    renderApp(<PluginRoutes />, { path: "/x/queue" });
    stream.plugins([{ ...queue, generic: [null as unknown as GenericView] }]);
    expect(screen.getByRole("alert")).toHaveTextContent("This page failed to load.");
  });
});
