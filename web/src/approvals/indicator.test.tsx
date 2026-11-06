import { act, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import type { ApprovalInfo, Me } from "@/api/types";
import { me as meOf } from "@/settings/fixtures";
import { TopBar } from "@/shell/topbar";
import {
  envelope,
  type FakeEventSource,
  installEventSource,
  renderApp,
  stubFetch,
} from "@/test/harness";
import { ApprovalsIndicator } from "./indicator";

let ES: typeof FakeEventSource;

beforeEach(() => {
  ES = installEventSource();
});

afterEach(() => {
  stopStream();
});

function connect(): FakeEventSource {
  startStream();
  const es = ES.instances.at(-1);
  if (!es) throw new Error("no EventSource");
  es.open();
  return es;
}

function request(id: string, over: Partial<ApprovalInfo> = {}): ApprovalInfo {
  return {
    id,
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
    input: {},
    inputSha256: "ab".repeat(32),
    destructive: true,
    confirm: "dialog",
    createdAt: "2026-11-06T10:00:00Z",
    expiresAt: "2026-11-06T10:10:00Z",
    decidedAt: null,
    decidedBy: "",
    reason: "",
    ...over,
  };
}

/** alice in the oidc mode, on a dash that offers her approvals (or not). */
function meWith(approvals = true): Me {
  const base = meOf("admin", { authMode: "oidc" });
  return { ...base, capabilities: { ...base.capabilities, mcp: true, approvals } };
}

const approvals = (pending: ApprovalInfo[]) => envelope("core.approvals", { pending });

describe("ApprovalsIndicator", () => {
  it("shows the pending count and lists each request, linking to its page", async () => {
    const es = connect();
    renderApp(<ApprovalsIndicator me={meWith()} />);
    act(() =>
      es.emit(
        "core.approvals",
        approvals([
          request("appr_A"),
          request("appr_B", {
            title: "Silence alert",
            plugin: "alertmanager",
            clientName: "",
            credentialName: "Agent",
            target: { kind: "Silence", namespace: "", name: "HostDown" },
          }),
        ]),
      ),
    );
    const trigger = screen.getByRole("button", { name: "2 pending approvals" });
    expect(trigger).toHaveTextContent("2");

    await userEvent.setup().click(trigger);
    const items = screen.getAllByRole("menuitem");
    expect(items.map((i) => i.getAttribute("href"))).toEqual([
      "/approve/appr_A",
      "/approve/appr_B",
    ]);
    expect(items[0]).toHaveTextContent("Restart workload media/jellyfin");
    // The credential names the request; the client's self-reported name is the page's.
    expect(items[0]).toHaveTextContent("laptop agent · kubernetes");
    expect(items[0]).not.toHaveTextContent("Claude Code");
    expect(items[1]).toHaveTextContent("Silence alert HostDown");
    expect(items[1]).toHaveTextContent("Agent · alertmanager");
  });

  it("follows the topic: one request is singular, and none hides it", () => {
    const es = connect();
    renderApp(<ApprovalsIndicator me={meWith()} />);
    act(() => es.emit("core.approvals", approvals([request("appr_A")])));
    expect(screen.getByRole("button", { name: "1 pending approval" })).toBeInTheDocument();
    act(() => es.emit("core.approvals", approvals([])));
    expect(screen.queryByRole("button", { name: /pending approval/ })).toBeNull();
  });

  it("shows nothing before the topic ever arrives", () => {
    connect();
    renderApp(<ApprovalsIndicator me={meWith()} />);
    expect(screen.queryByRole("button", { name: /pending approval/ })).toBeNull();
  });

  it("shows nothing to a principal dash offers no approvals", () => {
    const es = connect();
    renderApp(<ApprovalsIndicator me={meWith(false)} />);
    act(() => es.emit("core.approvals", approvals([request("appr_A")])));
    expect(screen.queryByRole("button", { name: /pending approval/ })).toBeNull();
  });

  it("is in the topbar", async () => {
    stubFetch({ "GET /api/v1/me": meWith(), "GET /api/v1/plugins": () => new Promise(() => {}) });
    const es = connect();
    renderApp(<TopBar />);
    act(() => es.emit("core.approvals", approvals([request("appr_A")])));
    expect(await screen.findByRole("button", { name: "1 pending approval" })).toBeInTheDocument();
  });
});
