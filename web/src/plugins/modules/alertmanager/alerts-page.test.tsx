import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ActionInfo, Alert, AlertsData, OverviewData, PluginInfo, Silence } from "@/api/types";
import { ui } from "@/api/ui-store";
import { GlobalDialogs } from "@/components/dialogs";
import { contributesPanel, Slot } from "@/plugins/slots";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { AlertsPage } from "./alerts-page";
import { AlertsBadge } from "./badge";

// Motion reads prefers-reduced-motion once; with it on a KPI tile prints its value at once.
const matchMedia = window.matchMedia;
window.matchMedia = (query: string) => ({
  ...matchMedia(query),
  matches: query.includes("prefers-reduced-motion"),
});

const action = (name: string, minRole = "admin"): ActionInfo => ({
  name,
  title: name,
  destructive: false,
  idempotent: name === "silence.expire",
  confirm: "none",
  minRole,
  inputSchema: null,
});

const BOTH = [action("silence.create"), action("silence.expire")];

const row = (actions: ActionInfo[] = BOTH, state = "healthy"): PluginInfo =>
  pluginInfo({
    id: "alertmanager",
    state,
    topics: ["alertmanager.alerts"],
    actions,
    nav: [{ path: "/alerts", label: "Alerts", title: "Alerts and silences", icon: "BellRing" }],
  });

const alert = (fp: string, name: string, over: Partial<Alert> = {}): Alert => ({
  ref: `alertmanager:Alert/${fp}`,
  protected: false,
  fingerprint: fp,
  name,
  severity: "warning",
  state: "active",
  summary: `${name} summary`,
  description: "",
  labels: { alertname: name, severity: "warning" },
  startsAt: "2026-01-01T00:00:00Z",
  silencedBy: [],
  inhibitedBy: [],
  ...over,
});

const silence = (id: string, dash: boolean, comment: string): Silence => ({
  ref: `alertmanager:Silence/${id}`,
  dash,
  id,
  matchers: [{ name: "alertname", value: "Muted", isRegex: false, isEqual: true }],
  createdBy: dash ? "dash:alice" : "carol@example.com",
  comment,
  startsAt: "2026-01-01T00:00:00Z",
  endsAt: "2026-01-02T00:00:00Z",
  state: "active",
});

const data = (over: Partial<AlertsData> = {}): AlertsData => ({
  alerts: [alert("f1", "DiskFull"), alert("f2", "Watchdog", { protected: true, severity: "none" })],
  silences: [silence("s-dash", true, "ours"), silence("s-foreign", false, "theirs")],
  silenceDurations: ["1h", "4h"],
  ...over,
});

const me = (role: string) => ({ user: "u", role, scopes: ["read"], ui: { iconBaseUrl: "" } });

/** The card (a Panel) that holds an element: its buttons are the controls of that one object. */
const cardOf = (el: HTMLElement): HTMLElement => {
  const card = el.closest<HTMLElement>(".space-y-3");
  if (!card) throw new Error("no card around the element");
  return card;
};

describe("the alerts page", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => stream.close());

  async function show(role: string, rows: PluginInfo[], payload: AlertsData) {
    const stub = stubFetch({ "GET /api/v1/me": me(role) });
    renderApp(<AlertsPage />, { path: "/alerts" });
    stream.plugins(rows);
    stream.send("alertmanager.alerts", payload);
    await screen.findByText("DiskFull");
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    await act(async () => {}); // /me has been read into the query cache
  }

  const silenceButtons = () => screen.queryAllByRole("button", { name: "Silence" });
  const expireButtons = () => screen.queryAllByRole("button", { name: "Expire" });

  it("offers Silence on an alert dash may silence, never on a protected one", async () => {
    await show("admin", [row()], data());
    await waitFor(() => expect(silenceButtons()).toHaveLength(1));
    expect(
      within(cardOf(screen.getByText("DiskFull"))).getByRole("button", { name: "Silence" }),
    ).toBeTruthy();
    expect(within(cardOf(screen.getByText("Watchdog"))).queryByRole("button")).toBeNull();
  });

  it("offers Expire only on the silences dash created", async () => {
    await show("admin", [row()], data());
    await userEvent.click(screen.getByRole("tab", { name: /Silences/ }));
    await waitFor(() => expect(expireButtons()).toHaveLength(1));
    expect(
      within(cardOf(screen.getByText("ours"))).getByRole("button", { name: "Expire" }),
    ).toBeTruthy();
    expect(within(cardOf(screen.getByText("theirs"))).queryByRole("button")).toBeNull();
  });

  it("offers neither to a viewer, though the row lists the actions", async () => {
    await show("viewer", [row()], data());
    expect(silenceButtons()).toHaveLength(0);
    await userEvent.click(screen.getByRole("tab", { name: /Silences/ }));
    await screen.findByText("ours");
    expect(expireButtons()).toHaveLength(0);
  });

  it("offers neither while the row lists no action (actions.enabled is off)", async () => {
    await show("admin", [row([])], data());
    expect(silenceButtons()).toHaveLength(0);
    await userEvent.click(screen.getByRole("tab", { name: /Silences/ }));
    await screen.findByText("ours");
    expect(expireButtons()).toHaveLength(0);
  });

  it("follows a live switch of actions.enabled", async () => {
    await show("admin", [row([])], data());
    expect(silenceButtons()).toHaveLength(0);
    stream.plugins([row()]);
    await waitFor(() => expect(silenceButtons()).toHaveLength(1));
    stream.plugins([row([])]);
    await waitFor(() => expect(silenceButtons()).toHaveLength(0));
  });

  it("offers no Silence without a duration to offer", async () => {
    await show("admin", [row()], data({ silenceDurations: undefined }));
    expect(silenceButtons()).toHaveLength(0);
  });
});

describe("the overview tile and the rail badge", () => {
  let stream: TestStream;
  beforeEach(() => {
    stubFetch({ "GET /api/v1/me": me("admin") });
    stream = connectStream();
  });
  afterEach(() => stream.close());

  const overview = (value: number, health: string): OverviewData =>
    ({
      status: "ok",
      attention: [],
      kpiList: [
        { key: "firing", plugin: "alertmanager", label: "Alerts firing", unit: "", value, health },
      ],
      topology: { host: {}, vms: [], groups: [] },
    }) as unknown as OverviewData;

  it("draws one tile from the firing KPI", async () => {
    renderApp(<Slot id="overview.kpis" data={overview(3, "crit")} />);
    stream.plugins([row()]);
    const tile = await screen.findByRole("link", { name: "Alerts firing: 3. Open alerts" });
    expect(screen.getAllByRole("link")).toHaveLength(1);
    expect(tile.getAttribute("href")).toBe("/alerts");
    expect(tile.textContent).toContain("3");
  });

  it("draws nothing while the KPI is not published", () => {
    renderApp(<Slot id="overview.kpis" data={{ ...overview(0, "ok"), kpiList: [] }} />);
    stream.plugins([row()]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("is hidden with its plugin", () => {
    renderApp(<Slot id="overview.kpis" data={overview(2, "warn")} />);
    stream.plugins([row([], "unavailable")]);
    expect(screen.queryAllByRole("link")).toHaveLength(0);
  });

  it("badges the rail with the KPI, and with nothing at zero", async () => {
    const { container } = renderApp(<AlertsBadge />);
    stream.send("core.overview", overview(2, "warn"));
    expect(await screen.findByText("2")).toBeTruthy();
    stream.send("core.overview", overview(0, "ok"));
    await waitFor(() => expect(screen.queryByText("2")).toBeNull());
    expect(container.textContent).toBe("");
  });

  // The plugin owns the presentation of its KPIs, so the overview draws no second tile for it.
  it("claims the plugin's overview tile", () => {
    expect(contributesPanel("alertmanager", "overview.kpis")).toBe(true);
  });
});

describe("the silence and expire dialogs", () => {
  let stream: TestStream;
  beforeEach(() => {
    stream = connectStream();
  });
  afterEach(() => {
    act(() => {
      ui.closeSilence();
      ui.closeExpire();
    });
    stream.close();
  });

  const CREATE = "POST /api/v1/plugins/alertmanager/actions/silence.create";
  const EXPIRE = "POST /api/v1/plugins/alertmanager/actions/silence.expire";

  async function openSilence(durations: string[] | undefined) {
    const stub = stubFetch({ [CREATE]: { silenceID: "new-id" }, [EXPIRE]: { status: "accepted" } });
    renderApp(<GlobalDialogs />);
    stream.plugins([row()]);
    stream.send("alertmanager.alerts", data({ silenceDurations: durations }));
    act(() => ui.openSilence(alert("f1", "DiskFull")));
    await screen.findByText("Silence DiskFull");
    return stub;
  }

  it("offers the plugin's durations, 4h first when it is one of them, and posts to the action", async () => {
    const stub = await openSilence(["1h", "4h", "7d"]);
    expect(screen.getByRole("combobox")).toHaveTextContent("4h");
    await userEvent.type(screen.getByLabelText(/Comment/), "  planned work ");
    await userEvent.click(screen.getByRole("button", { name: "Create silence" }));
    await waitFor(() => expect(stub.callsTo(CREATE)).toHaveLength(1));
    expect(stub.callsTo(CREATE)[0]?.body).toEqual({
      matchers: [{ name: "alertname", value: "DiskFull" }],
      duration: "4h",
      comment: "planned work",
    });
  });

  it("starts at the first duration when 4h is not offered, and never offers one the plugin does not", async () => {
    await openSilence(["1h", "24h"]);
    const select = screen.getByRole("combobox");
    expect(select).toHaveTextContent("1h");
    await userEvent.click(select);
    const options = (await screen.findAllByRole("option")).map((o) => o.textContent);
    expect(options).toEqual(["1h", "24h"]);
  });

  it("expires a silence by posting its id to the action, not by DELETE", async () => {
    const stub = stubFetch({ [EXPIRE]: { status: "accepted" } });
    renderApp(<GlobalDialogs />);
    act(() => ui.openExpire(silence("s-dash", true, "ours")));
    await userEvent.click(await screen.findByRole("button", { name: "Expire" }));
    await waitFor(() => expect(stub.callsTo(EXPIRE)).toHaveLength(1));
    expect(stub.callsTo(EXPIRE)[0]?.body).toEqual({ id: "s-dash" });
    expect(stub.calls.filter((c) => c.method === "DELETE")).toHaveLength(0);
  });
});
