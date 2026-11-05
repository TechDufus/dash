import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type * as Registry from "@/plugins/registry";
import type { SettingsFormProps } from "@/plugins/types";
import { renderApp, stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import { checkFailed, checkNotConfigured, settingsRoutes } from "./fixtures";
import { PluginSettingsPage } from "./plugin-settings-page";
import { K8S_SAVE_FIRST } from "./schema-form";

// hardware gets a form of its own; proxmox keeps the generic one.
vi.mock("@/plugins/registry", async (importOriginal) => {
  const orig = await importOriginal<typeof Registry>();
  // A vi.mock factory is hoisted above the file's static imports, so it loads React itself.
  const { createElement, lazy } = await import("react");
  const own = lazy(async () => ({
    default: ({ view }: SettingsFormProps) =>
      createElement("p", null, `own form, revision ${view.revision}`),
  }));
  return {
    ...orig,
    moduleById: new Map(
      [...orig.moduleById].map(([id, m]) => [id, id === "hardware" ? { ...m, settings: own } : m]),
    ),
  };
});

describe("PluginSettingsPage", () => {
  it("renders a module's own settings form instead of the generic one", async () => {
    stubFetch(settingsRoutes());
    renderApp(<PluginSettingsPage id="hardware" />);
    expect(await screen.findByText("own form, revision 7")).toBeInTheDocument();
    expect(screen.queryByRole("form")).toBeNull();
  });

  it("says save first when a typed k8s: reference tests not_configured (ruling 6)", async () => {
    const stub = stubFetch(
      settingsRoutes({ [`POST ${settingsPaths.test("proxmox")}`]: checkNotConfigured() }),
    );
    const user = userEvent.setup();
    renderApp(<PluginSettingsPage id="proxmox" />);
    await user.click(await screen.findByRole("button", { name: "Replace tokenSecret" }));
    await user.type(screen.getByLabelText("New tokenSecret"), "k8s:dash/pve#token");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    const result = await screen.findByRole("region", { name: "Connection test" });
    expect(result).toHaveTextContent(K8S_SAVE_FIRST);
    expect(stub.callsTo(`POST ${settingsPaths.test("proxmox")}`)[0]?.body).toEqual({
      secrets: { tokenSecret: "k8s:dash/pve#token" },
    });
  });

  it("does not say save first for a typed value or another failure", async () => {
    const stub = stubFetch(
      settingsRoutes({ [`POST ${settingsPaths.test("proxmox")}`]: checkNotConfigured() }),
    );
    const user = userEvent.setup();
    renderApp(<PluginSettingsPage id="proxmox" />);
    await user.click(await screen.findByRole("button", { name: "Replace tokenSecret" }));
    await user.type(screen.getByLabelText("New tokenSecret"), "a-value");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    const result = await screen.findByRole("region", { name: "Connection test" });
    expect(within(result).getByText(/not_configured/)).toBeInTheDocument();
    expect(result).not.toHaveTextContent(K8S_SAVE_FIRST);

    stub.route(`POST ${settingsPaths.test("proxmox")}`, checkFailed());
    await user.clear(screen.getByLabelText("New tokenSecret"));
    await user.type(screen.getByLabelText("New tokenSecret"), "k8s:dash/pve#token");
    await user.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(/auth: the credentials were refused/)).toBeInTheDocument();
    expect(screen.queryByText(K8S_SAVE_FIRST)).toBeNull();
  });
});
