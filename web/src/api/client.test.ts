import { afterEach, beforeEach, describe, expect, it, type MockInstance, vi } from "vitest";
import { browserNav } from "@/auth/handle401";
import { json, stubFetch } from "@/test/harness";
import { ApiError, api } from "./client";

// The client hands every refused response to handle401 (auth/handle401.ts); these tests go
// through the real handle401 and watch the navigation it makes, so dropping the call fails them.
let reload: MockInstance<() => void>;
let assign: MockInstance<(url: string) => void>;

beforeEach(() => {
  sessionStorage.clear();
  window.history.replaceState(null, "", "/pods?ns=a");
  reload = vi.spyOn(browserNav, "reload").mockImplementation(() => {});
  assign = vi.spyOn(browserNav, "assign").mockImplementation(() => {});
});

afterEach(() => {
  sessionStorage.clear();
  window.history.replaceState(null, "", "/");
});

function unauthenticated(details: unknown) {
  return () => json({ error: "sign in", code: "unauthenticated", details }, 401);
}

describe("api client: a 401 goes to handle401", () => {
  it("reloads behind the identity proxy and still rejects with the ApiError", async () => {
    stubFetch({ "GET /api/v1/pods": unauthenticated({ mode: "proxy", setup: false }) });
    const err = await api.get("/api/v1/pods").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).status).toBe(401);
    expect(reload).toHaveBeenCalledTimes(1);
    expect(assign).not.toHaveBeenCalled();
  });

  it("sends the local mode to /login with the current page as next", async () => {
    stubFetch({ "POST /api/v1/x": unauthenticated({ mode: "local", setup: false }) });
    await expect(api.post("/api/v1/x")).rejects.toBeInstanceOf(ApiError);
    expect(assign).toHaveBeenCalledWith(`/login?next=${encodeURIComponent("/pods?ns=a")}`);
  });

  it("reloads for a bare 401 that is not dash's answer", async () => {
    stubFetch({ "GET /api/v1/pods": () => new Response("", { status: 401 }) });
    await expect(api.get("/api/v1/pods")).rejects.toBeInstanceOf(ApiError);
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it.each([403, 404, 500])("does not navigate on a %i", async (status) => {
    stubFetch({ "GET /api/v1/pods": () => json({ error: "no", code: "x" }, status) });
    await expect(api.get("/api/v1/pods")).rejects.toBeInstanceOf(ApiError);
    expect(reload).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });
});

describe("api client: writes and text", () => {
  it("sends a PUT with its JSON body, the CSRF header and the revision as If-Match", async () => {
    const f = stubFetch({
      "PUT /api/v1/settings/plugins/proxmox": { revision: 8, restartRequired: [] },
    });
    const out = await api.put<{ revision: number }>(
      "/api/v1/settings/plugins/proxmox",
      { node: "pve" },
      { ifMatch: 7 },
    );
    expect(out.revision).toBe(8);
    const [call] = f.callsTo("PUT /api/v1/settings/plugins/proxmox");
    expect(call?.body).toEqual({ node: "pve" });
    expect(call?.headers.get("If-Match")).toBe('"7"');
    expect(call?.headers.get("X-Dash-Csrf")).toBe("1");
    expect(call?.headers.get("Content-Type")).toBe("application/json");
  });

  it("sends no If-Match without a revision, and a DELETE may carry one", async () => {
    const f = stubFetch({ "PUT /x": {}, "DELETE /y": undefined });
    await api.put("/x", {});
    await api.del("/y", { ifMatch: 3 });
    expect(f.callsTo("PUT /x")[0]?.headers.has("If-Match")).toBe(false);
    expect(f.callsTo("DELETE /y")[0]?.headers.get("If-Match")).toBe('"3"');
  });

  it("answers a body that is not JSON as text", async () => {
    const yaml = "plugins:\n  proxmox:\n    node: pve\n";
    stubFetch({
      "GET /api/v1/settings/export": () =>
        new Response(yaml, { headers: { "Content-Type": "text/yaml" } }),
    });
    await expect(api.text("/api/v1/settings/export")).resolves.toBe(yaml);
  });

  it("rejects a refused PUT with the envelope's code and details", async () => {
    const details = { path: "plugins.proxmox.url", source: "env", ref: "DASH_PLUGINS_PROXMOX_URL" };
    stubFetch({
      "PUT /x": () =>
        json({ error: "locked", code: "locked_field", requestId: "r1", details }, 409),
    });
    const err = (await api.put("/x", {}, { ifMatch: 1 }).catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect([err.status, err.code, err.requestId]).toEqual([409, "locked_field", "r1"]);
    expect(err.details).toEqual(details);
  });
});
