import { screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { startStream, stopStream } from "@/api/stream";
import { installEventSource, pluginsEnvelope, renderApp, stubFetch } from "@/test/harness";
import { shimRows } from "@/test/shims";
import { PluginRoutes } from "./routes";

vi.mock("@/pages/status", () => ({
  StatusPage: () => {
    throw new Error("boom");
  },
}));

describe("a page that throws", () => {
  beforeEach(() => {
    const ES = installEventSource();
    stubFetch();
    startStream();
    const es = ES.instances[0];
    es?.open();
    es?.emit("core.plugins", pluginsEnvelope(shimRows()));
  });

  afterEach(() => {
    stopStream();
  });

  it("fails alone: the route shows a reload prompt and the error is logged", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    renderApp(<PluginRoutes />, { path: "/status" });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("This page failed to load.");
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
    expect(error.mock.calls.some((c) => String(c[0]).includes("a page failed"))).toBe(true);
    error.mockRestore();
  });
});
