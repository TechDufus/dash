import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, type MockInstance, vi } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import type { ApprovalInfo, Me } from "@/api/types";
import { browserNav } from "@/auth/handle401";
import { PluginRoutes } from "@/plugins/routes";
import { me as meOf, settingsError } from "@/settings/fixtures";
import { envelope, installEventSource, json, renderApp, stubFetch } from "@/test/harness";
import {
  ApprovePage,
  CLIENT_UNREPORTED,
  EXPIRED,
  FOREIGN,
  formatCountdown,
  GONE,
  MALFORMED,
  TITLE,
} from "./approve-page";

const ID = "appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ";
const GET = `GET /api/v1/approvals/${ID}`;
const POST = `POST /api/v1/approvals/${ID}/decision`;

/** A pending restart of media/jellyfin that expires `inMs` from now. */
function approval(over: Partial<ApprovalInfo> = {}, inMs = 9 * 60_000): ApprovalInfo {
  return {
    id: ID,
    state: "pending",
    user: "alice",
    tokenId: "tok_1",
    credentialName: "laptop agent",
    clientName: "Claude Code",
    plugin: "kubernetes",
    action: "restart",
    title: "Restart workload",
    tool: "kubernetes_restart",
    summary: "Restart Deployment media/jellyfin",
    target: { kind: "Deployment", namespace: "media", name: "jellyfin" },
    input: { kind: "Deployment", namespace: "media", name: "jellyfin" },
    inputSha256: "ab".repeat(32),
    destructive: true,
    confirm: "dialog",
    createdAt: new Date(Date.now() - 60_000).toISOString(),
    expiresAt: new Date(Date.now() + inMs).toISOString(),
    decidedAt: null,
    decidedBy: "",
    reason: "",
    ...over,
  };
}

function approved(over: Partial<ApprovalInfo> = {}): ApprovalInfo {
  return approval({
    state: "approved",
    decidedAt: "2026-11-06T10:01:00Z",
    decidedBy: "alice",
    outcome: {
      ok: true,
      result: '{"restartedAt":"2026-11-06T10:01:00Z"}',
      truncated: false,
      error: "",
      at: "2026-11-06T10:01:01Z",
    },
    ...over,
  });
}

/** alice, an administrator in the oidc mode, on a dash that offers her approvals. */
function meWith(over: Partial<Me> = {}): Me {
  const base = meOf("admin", { authMode: "oidc" });
  return { ...base, capabilities: { ...base.capabilities, mcp: true, approvals: true }, ...over };
}

function routes(over: Record<string, unknown> = {}) {
  return { "GET /api/v1/me": meWith(), [GET]: approval(), [POST]: approved(), ...over };
}

let assign: MockInstance<(url: string) => void>;

beforeEach(() => {
  assign = vi.spyOn(browserNav, "assign").mockImplementation(() => {});
  window.history.replaceState(null, "", "/");
});

afterEach(() => {
  stopStream();
});

describe("formatCountdown", () => {
  it("counts minutes and seconds, rounding up and never below zero", () => {
    expect(formatCountdown(9 * 60_000 + 41_500)).toBe("9:42");
    expect(formatCountdown(5_000)).toBe("0:05");
    expect(formatCountdown(0)).toBe("0:00");
    expect(formatCountdown(-3_000)).toBe("0:00");
  });
});

describe("ApprovePage", () => {
  it("shows the client, credential, action, target, summary, input and a countdown", async () => {
    stubFetch(routes());
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByRole("heading", { name: /Restart workload/ })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: TITLE })).toBeInTheDocument();
    expect(document.title).toBe("Approval");
    expect(screen.getByText("· kubernetes")).toBeInTheDocument();
    expect(screen.getByText("Destructive")).toBeInTheDocument();
    expect(screen.getByText("Restart Deployment media/jellyfin")).toBeInTheDocument();
    // The client's name is its own claim; the credential is what dash knows.
    expect(screen.getByText("(as it names itself)").parentElement).toHaveTextContent(
      "Claude Code (as it names itself)",
    );
    expect(screen.getByText("Token")).toBeInTheDocument();
    expect(screen.getByText("laptop agent")).toBeInTheDocument();
    expect(screen.getByText("kubernetes_restart")).toBeInTheDocument();
    expect(screen.getByText("Deployment media/jellyfin")).toBeInTheDocument();
    const input = screen.getByLabelText("Input");
    expect(JSON.parse(input.textContent ?? "")).toEqual({
      kind: "Deployment",
      namespace: "media",
      name: "jellyfin",
    });
    expect(screen.getByRole("timer")).toHaveTextContent(/^Expires in (9:0\d|8:5\d)$/);
    expect(screen.queryByLabelText(/to approve/)).toBeNull();
  });

  it("names an OAuth grant a connected app, an unreported client muted, and drops the badge", async () => {
    stubFetch(
      routes({
        [GET]: approval({
          tokenId: undefined,
          grantId: "grant_1",
          credentialName: "Agent",
          clientName: "",
          destructive: false,
        }),
      }),
    );
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByText("Connected app")).toBeInTheDocument();
    expect(screen.getByText("Agent")).toBeInTheDocument();
    expect(screen.getByText(CLIENT_UNREPORTED)).toHaveClass("text-muted");
    expect(screen.queryByText(/as it names itself/)).toBeNull();
    expect(screen.queryByText("Destructive")).toBeNull();
  });

  it("approves with the CSRF header and no confirm, then shows the outcome", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes());
    renderApp(<ApprovePage id={ID} />);
    await user.click(await screen.findByRole("button", { name: "Approve" }));

    expect(await screen.findByText("The action ran")).toBeInTheDocument();
    await waitFor(() => expect(stub.callsTo(POST)).toHaveLength(1));
    const [post] = stub.callsTo(POST);
    expect(post?.body).toEqual({ approve: true });
    expect(post?.headers.get("X-Dash-Csrf")).toBe("1");
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(screen.queryByRole("timer")).toBeNull();
    // The result is there, collapsed.
    const result = screen.getByLabelText("Result");
    expect(result).not.toBeVisible();
    await user.click(screen.getByText("Result"));
    expect(result).toBeVisible();
    expect(JSON.parse(result.textContent ?? "")).toEqual({ restartedAt: "2026-11-06T10:01:00Z" });
  });

  it("shows an action that failed after approval", async () => {
    const user = userEvent.setup();
    stubFetch(
      routes({
        [POST]: approved({
          outcome: {
            ok: false,
            result: "",
            truncated: false,
            error: "deployment not found",
            at: "2026-11-06T10:01:01Z",
          },
        }),
      }),
    );
    renderApp(<ApprovePage id={ID} />);
    await user.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText("The action failed")).toBeInTheDocument();
    expect(screen.getByText("deployment not found")).toBeInTheDocument();
    expect(screen.queryByText("Result")).toBeNull();
  });

  it("denies, then shows the denied state", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(
      routes({
        [POST]: approval({
          state: "denied",
          decidedAt: "2026-11-06T10:01:00Z",
          decidedBy: "alice",
        }),
      }),
    );
    renderApp(<ApprovePage id={ID} />);
    await user.click(await screen.findByRole("button", { name: "Deny" }));
    const status = await screen.findByRole("status");
    expect(within(status).getByText("Denied")).toBeInTheDocument();
    expect(within(status).getByText(/Denied by alice.*did not run/)).toBeInTheDocument();
    await waitFor(() => expect(stub.callsTo(POST)).toHaveLength(1));
    expect(stub.callsTo(POST)[0]?.body).toEqual({ approve: false });
  });

  it("gates Approve on the typed target name, and sends it as confirm", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes({ [GET]: approval({ confirm: "typeName" }) }));
    renderApp(<ApprovePage id={ID} />);
    const field = await screen.findByLabelText("Type the Deployment's name (jellyfin) to approve");
    const approve = screen.getByRole("button", { name: "Approve" });
    expect(approve).toBeDisabled();
    await user.type(field, "jellyfi");
    expect(approve).toBeDisabled();
    await user.type(field, "n");
    expect(approve).toBeEnabled();
    await user.click(approve);
    await waitFor(() => expect(stub.callsTo(POST)).toHaveLength(1));
    expect(stub.callsTo(POST)[0]?.body).toEqual({ approve: true, confirm: "jellyfin" });
  });

  it("denies a typed-name request without the name, and without confirm", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(
      routes({ [GET]: approval({ confirm: "typeName" }), [POST]: approval({ state: "denied" }) }),
    );
    renderApp(<ApprovePage id={ID} />);
    await user.click(await screen.findByRole("button", { name: "Deny" }));
    await waitFor(() => expect(stub.callsTo(POST)).toHaveLength(1));
    expect(stub.callsTo(POST)[0]?.body).toEqual({ approve: false });
  });

  it("shows the server's message when the typed name is refused", async () => {
    const user = userEvent.setup();
    const refused = "type the Deployment's name (jellyfin) to approve";
    stubFetch(
      routes({
        [GET]: approval({ confirm: "typeName" }),
        [POST]: settingsError(400, "invalid_input", refused),
      }),
    );
    renderApp(<ApprovePage id={ID} />);
    await user.type(
      await screen.findByLabelText("Type the Deployment's name (jellyfin) to approve"),
      "jellyfin",
    );
    await user.click(screen.getByRole("button", { name: "Approve" }));
    const alert = await screen.findByRole("alert");
    expect(within(alert).getByText(refused)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Approve" })).toBeEnabled();
  });

  it("shows a request decided already, with its outcome and nothing to decide", async () => {
    stubFetch(routes({ [GET]: approved() }));
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByText("The action ran")).toBeInTheDocument();
    expect(screen.getByText(/Approved by alice/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Deny" })).toBeNull();
  });

  it("shows why dash denied a request on its own", async () => {
    stubFetch(routes({ [GET]: approval({ state: "denied", reason: "the token was revoked" }) }));
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByText("dash denied it: the token was revoked")).toBeInTheDocument();
  });

  it("shows what a request decided elsewhere became (409)", async () => {
    const user = userEvent.setup();
    let reads = 0;
    stubFetch(
      routes({
        [GET]: () => (++reads === 1 ? approval() : approved()),
        [POST]: settingsError(409, "conflict", "the request is no longer pending"),
      }),
    );
    renderApp(<ApprovePage id={ID} />);
    await user.click(await screen.findByRole("button", { name: "Deny" }));
    expect(await screen.findByText("The action ran")).toBeInTheDocument();
  });

  it("says a request past its retention (410) is gone, and offers nothing to decide", async () => {
    stubFetch(routes({ [GET]: settingsError(410, "approval_expired", "approval expired") }));
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByText(GONE)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: TITLE })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
  });

  it("shows an expired request with its details, and nothing to decide", async () => {
    stubFetch(routes({ [GET]: approval({ state: "expired" }, -60_000) }));
    renderApp(<ApprovePage id={ID} />);
    const status = await screen.findByRole("status");
    expect(within(status).getByText("Expired")).toBeInTheDocument();
    expect(within(status).getByText(EXPIRED)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: /Restart workload/ })).toBeInTheDocument();
    expect(screen.getByText("laptop agent")).toBeInTheDocument();
    expect(screen.getByText("kubernetes_restart")).toBeInTheDocument();
    expect(screen.getByText("Deployment media/jellyfin")).toBeInTheDocument();
    expect(screen.getByText("Restart Deployment media/jellyfin")).toBeInTheDocument();
    expect(JSON.parse(screen.getByLabelText("Input").textContent ?? "")).toEqual({
      kind: "Deployment",
      namespace: "media",
      name: "jellyfin",
    });
    expect(screen.queryByRole("timer")).toBeNull();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Deny" })).toBeNull();
    expect(screen.queryByText(GONE)).toBeNull();
  });

  it("says the request is gone when the decision gets a 410", async () => {
    const user = userEvent.setup();
    stubFetch(routes({ [POST]: settingsError(410, "approval_expired", "approval expired") }));
    renderApp(<ApprovePage id={ID} />);
    await user.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText(GONE)).toBeInTheDocument();
  });

  it.each([
    ["too short", "appr_ABC"],
    ["lowercase", "appr_abcdefghijklmnopqrstuvwxyz"],
    ["outside base32", "appr_ABCDEFGHIJKLMNOPQRSTUVWX01"],
    ["another prefix", "tok_ABCDEFGHIJKLMNOPQRSTUVWXYZ"],
  ])("says an id %s is not an approval link, and requests nothing for it", async (_name, id) => {
    const stub = stubFetch(routes());
    renderApp(<ApprovePage id={id} />);
    expect(await screen.findByText(MALFORMED)).toBeInTheDocument();
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    expect(stub.calls.filter((c) => c.path.startsWith("/api/v1/approvals"))).toHaveLength(0);
    expect(assign).not.toHaveBeenCalled();
  });

  it("says a 404 is not an approval link", async () => {
    stubFetch(routes({ [GET]: settingsError(404, "not_found", "not found") }));
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByText(MALFORMED)).toBeInTheDocument();
    expect(screen.queryByText(GONE)).toBeNull();
  });

  it("reads the request again when core.approvals drops it, and shows what it became", async () => {
    const ES = installEventSource();
    startStream();
    const es = ES.instances.at(-1);
    if (!es) throw new Error("no EventSource");
    es.open();
    let reads = 0;
    const stub = stubFetch(
      routes({ [GET]: () => (++reads === 1 ? approval() : approved({ decidedBy: "alice" })) }),
    );
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByRole("button", { name: "Approve" })).toBeInTheDocument();
    const listed = { ...approval(), input: null };
    act(() => es.emit("core.approvals", envelope("core.approvals", { pending: [listed] })));
    expect(stub.callsTo(GET)).toHaveLength(1);
    // Approved on the phone: the list drops it, and the page reads it again.
    act(() => es.emit("core.approvals", envelope("core.approvals", { pending: [] })));
    expect(await screen.findByText("The action ran")).toBeInTheDocument();
    expect(stub.callsTo(GET)).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    // Another change of the list does not read it again: it is no longer pending.
    act(() => es.emit("core.approvals", envelope("core.approvals", { pending: [] })));
    expect(stub.callsTo(GET)).toHaveLength(2);
  });

  it("says another user's request belongs to another user", async () => {
    stubFetch(routes({ [GET]: settingsError(403, "forbidden", "forbidden") }));
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByText(FOREIGN)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
  });

  it("says the request belongs to another user when the decision gets a 403", async () => {
    const user = userEvent.setup();
    stubFetch(routes({ [POST]: settingsError(403, "forbidden", "forbidden") }));
    renderApp(<ApprovePage id={ID} />);
    await user.click(await screen.findByRole("button", { name: "Approve" }));
    expect(await screen.findByText(FOREIGN)).toBeInTheDocument();
  });

  it("counts down, and says the request expired when the countdown reaches zero", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    vi.setSystemTime(new Date("2026-11-06T10:00:00Z"));
    const stub = stubFetch(routes({ [GET]: approval({}, 3_000) }));
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByRole("timer")).toHaveTextContent("Expires in 0:03");
    act(() => vi.advanceTimersByTime(1_000));
    expect(screen.getByRole("timer")).toHaveTextContent("Expires in 0:02");
    act(() => vi.advanceTimersByTime(2_000));
    expect(await screen.findByText(EXPIRED)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
    expect(stub.callsTo(POST)).toHaveLength(0);
  });

  it("sends the anonymous viewer to sign in with the short next, and reads nothing", async () => {
    const stub = stubFetch(
      routes({
        "GET /api/v1/me": meWith({ authMode: "local", role: "viewer", user: "anonymous" }),
      }),
    );
    renderApp(<ApprovePage id={ID} />);
    await waitFor(() => expect(assign).toHaveBeenCalledWith(`/login?next=%2Fapprove%2F${ID}`));
    expect(stub.callsTo(GET)).toHaveLength(0);
  });

  it("sends a signed-out user to sign in with the short next", async () => {
    window.history.replaceState(null, "", `/approve/${ID}`);
    stubFetch(
      routes({
        "GET /api/v1/me": json(
          { error: "sign in", code: "unauthenticated", details: { mode: "local", setup: false } },
          401,
        ),
      }),
    );
    renderApp(<ApprovePage id={ID} />, { path: `/approve/${ID}` });
    await waitFor(() => expect(assign).toHaveBeenCalledWith(`/login?next=%2Fapprove%2F${ID}`));
  });

  it("says so, and reads nothing, where nobody can hold a token", async () => {
    const stub = stubFetch(routes({ "GET /api/v1/me": meOf("admin", { authMode: "none" }) }));
    renderApp(<ApprovePage id={ID} />);
    expect(await screen.findByText(/not available on this dash/)).toBeInTheDocument();
    expect(stub.callsTo(GET)).toHaveLength(0);
  });

  it("is a route of the app shell", async () => {
    stubFetch(routes());
    renderApp(<PluginRoutes />, { path: `/approve/${ID}` });
    expect(
      await screen.findByRole("heading", { name: /Restart workload/ }, { timeout: 3000 }),
    ).toBeInTheDocument();
  });
});
