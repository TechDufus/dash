import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import type { AuditEntry, SettingsAudit } from "@/api/types";
import { type FetchCall, renderApp, stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import { AuditPage } from "./audit-page";
import { auditMain, auditUnauth, me, settingsRoutes } from "./fixtures";

const AUDIT = `GET ${settingsPaths.audit}`;

/** The data rows of the audit table (the header row excluded). */
function rows(): HTMLElement[] {
  return screen.getAllByRole("row").slice(1);
}

function pressed(group: string, name: string) {
  return within(screen.getByRole("group", { name: group })).getByRole("button", { name });
}

/** An older page of the main ring, reachable only with the main ring's cursor. */
function olderMain(): SettingsAudit {
  const old: AuditEntry = {
    time: "2026-11-05T09:00:00Z",
    kind: "outcome",
    id: "a0",
    action: "flux.reconcile",
    target: { kind: "Kustomization", namespace: "flux-system", name: "apps" },
    result: "ok",
    plugin: "flux",
    actor: { type: "proxy", user: "alice", surface: "ui" },
  };
  return auditMain({ records: [old], next: undefined });
}

/** Answers each ring by its own cursor; any other combination is a test failure. */
function ringRoutes() {
  return settingsRoutes({
    [AUDIT]: ({ query }: FetchCall) => {
      const ring = query.get("ring");
      const before = query.get("before");
      if (ring === "main" && before === null) return auditMain();
      if (ring === "main" && before === "main.494") return olderMain();
      if (ring === "unauth" && before === null) return auditUnauth({ next: "unauth.199" });
      throw new Error(`unexpected audit request ring=${ring} before=${before}`);
    },
  });
}

describe("AuditPage", () => {
  it("shows settings and action events of the main ring, newest first", async () => {
    stubFetch(settingsRoutes());
    renderApp(<AuditPage />);
    await waitFor(() => expect(rows()).toHaveLength(6));
    const actions = rows().map((r) => within(r).getAllByRole("cell")[2]?.textContent);
    expect(actions).toEqual([
      "kubernetes.restart",
      "kubernetes.restart",
      "settings.test",
      "settings.secret.replace",
      "settings.set",
      "settings.set",
    ]);
    expect(screen.getAllByText("Deployment media/app")).toHaveLength(2);
    expect(screen.getAllByText("plugins.proxmox.url, plugins.proxmox.node")).toHaveLength(2);
  });

  it("switches to the unauth ring and back, each from its own newest page", async () => {
    const stub = stubFetch(ringRoutes());
    const user = userEvent.setup();
    renderApp(<AuditPage />);
    await waitFor(() => expect(rows()).toHaveLength(6));

    await user.click(pressed("Ring", "Refusals"));
    await waitFor(() => expect(rows()).toHaveLength(1));
    expect(rows()[0]).toHaveTextContent("GET /api/v1/settings");
    expect(rows()[0]).toHaveTextContent("forbidden");
    expect(pressed("Ring", "Refusals")).toHaveAttribute("aria-pressed", "true");

    await user.click(pressed("Ring", "Activity"));
    await waitFor(() => expect(rows()).toHaveLength(6));
    const asked = stub.callsTo(AUDIT).map((c) => `${c.query.get("ring")}:${c.query.get("before")}`);
    expect(asked).toContain("unauth:null");
    expect(asked.every((a) => a.endsWith(":null"))).toBe(true);
  });

  it("loads older records with the ring's next cursor and appends them", async () => {
    const stub = stubFetch(ringRoutes());
    const user = userEvent.setup();
    renderApp(<AuditPage />);
    await waitFor(() => expect(rows()).toHaveLength(6));

    await user.click(screen.getByRole("button", { name: "Load older" }));
    await waitFor(() => expect(rows()).toHaveLength(7));
    expect(rows()[6]).toHaveTextContent("flux.reconcile");
    const older = stub.callsTo(AUDIT).at(-1);
    expect(older?.query.get("ring")).toBe("main");
    expect(older?.query.get("before")).toBe("main.494");
    // The last page has no next: the end of the ring.
    expect(screen.queryByRole("button", { name: "Load older" })).toBeNull();
    expect(screen.getByText("No older records")).toBeInTheDocument();
  });

  it("never sends one ring's cursor to the other ring", async () => {
    const stub = stubFetch(ringRoutes());
    const user = userEvent.setup();
    renderApp(<AuditPage />);
    await waitFor(() => expect(rows()).toHaveLength(6));

    await user.click(pressed("Ring", "Refusals"));
    await waitFor(() => expect(rows()).toHaveLength(1));
    stub.route(AUDIT, ({ query }: FetchCall) => {
      expect(`${query.get("ring")}:${query.get("before")}`).toBe("unauth:unauth.199");
      return auditUnauth({ records: [], next: undefined });
    });
    await user.click(screen.getByRole("button", { name: "Load older" }));
    await waitFor(() => expect(screen.getByText("No older records")).toBeInTheDocument());
    const last = stub.callsTo(AUDIT).at(-1);
    expect(last?.query.get("ring")).toBe("unauth");
    expect(last?.query.get("before")).toBe("unauth.199");
  });

  it("filters by kind on the client, without another request", async () => {
    const stub = stubFetch(settingsRoutes());
    const user = userEvent.setup();
    renderApp(<AuditPage />);
    await waitFor(() => expect(rows()).toHaveLength(6));
    const before = stub.callsTo(AUDIT).length;

    await user.click(pressed("Kind", "Intent"));
    expect(rows()).toHaveLength(2);
    for (const r of rows()) expect(within(r).getAllByRole("cell")[1]).toHaveTextContent("intent");

    await user.click(pressed("Kind", "Event"));
    expect(rows()).toHaveLength(1);
    expect(rows()[0]).toHaveTextContent("settings.test");

    await user.click(pressed("Kind", "Denial"));
    expect(screen.queryAllByRole("row")).toHaveLength(0);
    expect(screen.getByText("No loaded records of this kind")).toBeInTheDocument();

    await user.click(pressed("Kind", "All"));
    expect(rows()).toHaveLength(6);
    expect(stub.callsTo(AUDIT)).toHaveLength(before);
  });

  it("names the source: the in-memory rings since this start", async () => {
    stubFetch(settingsRoutes());
    renderApp(<AuditPage />);
    expect(
      await screen.findByText("Source: since this start (no data directory)"),
    ).toBeInTheDocument();
  });

  it("names the source: the audit.jsonl file", async () => {
    stubFetch(settingsRoutes({ [AUDIT]: auditMain({ enabled: true, source: "file" }) }));
    renderApp(<AuditPage />);
    expect(await screen.findByText("Source: file audit.jsonl")).toBeInTheDocument();
  });

  it("shows an empty ring as empty, with nothing to load", async () => {
    stubFetch(settingsRoutes({ [AUDIT]: auditMain({ records: [], next: undefined }) }));
    renderApp(<AuditPage />);
    expect(await screen.findByText("No audit records yet")).toBeInTheDocument();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByRole("button", { name: "Load older" })).toBeNull();
  });

  it("shows a viewer Not authorised and reads no audit record", async () => {
    const stub = stubFetch(settingsRoutes({ "GET /api/v1/me": me("viewer") }));
    renderApp(<AuditPage />);
    expect(await screen.findByText(/Not authorised/)).toBeInTheDocument();
    expect(stub.callsTo(AUDIT)).toHaveLength(0);
  });
});
