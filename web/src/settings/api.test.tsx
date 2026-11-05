import { screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ApiError, api } from "@/api/client";
import { useMe } from "@/api/queries";
import { renderApp, stubFetch } from "@/test/harness";
import {
  fieldErrors,
  settingsErrorDetails,
  settingsPaths,
  useSettingsPlugin,
  useSettingsView,
} from "./api";
import { invalidFields, me, settingsRoutes } from "./fixtures";

function Probe({ plugin }: { plugin?: string }) {
  const meQ = useMe();
  const view = useSettingsView();
  const pv = useSettingsPlugin(plugin ?? "");
  return (
    <p>
      {meQ.data ? `me ${meQ.data.role}` : "loading"} view {view.data ? view.data.revision : "none"}{" "}
      plugin {pv.data ? pv.data.id : "none"}
    </p>
  );
}

describe("settings queries", () => {
  it("never ask a viewer's settings", async () => {
    const f = stubFetch(settingsRoutes({ "GET /api/v1/me": me("viewer") }));
    renderApp(<Probe plugin="proxmox" />);
    await screen.findByText(/me viewer/);
    expect(f.calls.filter((c) => c.path.startsWith("/api/v1/settings"))).toEqual([]);
  });

  it("read the view and a plugin for an administrator", async () => {
    const f = stubFetch(settingsRoutes());
    renderApp(<Probe plugin="proxmox" />);
    await screen.findByText(/view 7 plugin proxmox/);
    expect(f.callsTo(`GET ${settingsPaths.view}`)).toHaveLength(1);
    expect(f.callsTo(`GET ${settingsPaths.plugin("proxmox")}`)).toHaveLength(1);
  });

  it("encode a field as one path segment, dots kept", () => {
    expect(settingsPaths.secret("prometheus", "auth.bearerToken")).toBe(
      "/api/v1/settings/plugins/prometheus/secrets/auth.bearerToken",
    );
  });
});

describe("settings errors", () => {
  it("read the details and the field problems of a refused write", async () => {
    stubFetch({
      "PUT /x": () =>
        invalidFields([
          { path: "plugins.proxmox.url", message: "must be an http(s) URL" },
          { path: "plugins.proxmox.url", message: "a second problem" },
        ]),
    });
    const err = await api.put("/x", {}, { ifMatch: 7 }).catch((e: unknown) => e);
    await waitFor(() => expect(err).toBeInstanceOf(ApiError));
    expect(settingsErrorDetails(err)?.fields).toHaveLength(2);
    expect([...fieldErrors(err)]).toEqual([["plugins.proxmox.url", "must be an http(s) URL"]]);
    expect(settingsErrorDetails(new Error("x"))).toBeUndefined();
    expect(fieldErrors(undefined).size).toBe(0);
  });
});
