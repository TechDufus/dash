import { screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "@/api/queries";
import { installEventSource, json, renderApp, stubFetch } from "@/test/harness";
import { browserNav } from "./handle401";
import { isPublicPath, PublicApp } from "./public-app";

// The public shell must not load the event stream or the plugin state: importing either fails.
vi.mock("@/api/stream", () => {
  throw new Error("the public shell imported the event stream");
});
vi.mock("@/plugins/state", () => {
  throw new Error("the public shell imported the plugin state");
});

const ME_401 = () =>
  json(
    {
      error: "authentication required",
      code: "unauthenticated",
      details: { mode: "local", setup: false },
    },
    401,
  );

afterEach(() => {
  queryClient.clear();
});

describe("PublicApp", () => {
  it.each(["/login", "/login/", "/login/anything"])("serves the login page on %s", async (path) => {
    stubFetch({ "GET /api/v1/me": ME_401 });
    renderApp(<PublicApp />, { path });
    expect(await screen.findByRole("heading", { name: "Sign in to dash" })).toBeInTheDocument();
  });

  it.each(["/setup", "/setup/anything"])("serves the setup page on %s", async (path) => {
    stubFetch();
    renderApp(<PublicApp />, { path });
    expect(await screen.findByRole("heading", { name: "Set up dash" })).toBeInTheDocument();
  });

  it("has its own Toaster", async () => {
    stubFetch({ "GET /api/v1/me": ME_401 });
    renderApp(<PublicApp />, { path: "/login" });
    expect(await screen.findByRole("region", { name: /notifications/i })).toBeInTheDocument();
  });

  it("opens no event stream and fetches nothing but the sign-in probe", async () => {
    const ES = installEventSource();
    const stub = stubFetch({ "GET /api/v1/me": ME_401 });
    renderApp(<PublicApp />, { path: "/login" });
    await screen.findByLabelText("Username");
    await waitFor(() => expect(stub.calls).toHaveLength(1));
    expect(stub.calls.map((c) => `${c.method} ${c.path}`)).toEqual(["GET /api/v1/me"]);
    expect(ES.instances).toHaveLength(0);
  });

  it("does not reload or redirect on the probe's 401", async () => {
    const reload = vi.spyOn(browserNav, "reload").mockImplementation(() => {});
    const assign = vi.spyOn(browserNav, "assign").mockImplementation(() => {});
    stubFetch({ "GET /api/v1/me": ME_401 });
    renderApp(<PublicApp />, { path: "/login" });
    await screen.findByLabelText("Username");
    expect(reload).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });
});

describe("isPublicPath", () => {
  it.each([
    ["/login", true],
    ["/login/", true],
    ["/login/x", true],
    ["/setup", true],
    ["/setup/x", true],
    ["/", false],
    ["/loginx", false],
    ["/setupx", false],
    ["/pods", false],
    ["/x/login", false],
    ["/auth/oidc/start", false],
  ])("%s -> %s", (path, want) => {
    expect(isPublicPath(path)).toBe(want);
  });
});
