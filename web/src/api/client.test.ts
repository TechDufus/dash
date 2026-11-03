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
