import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { renderApp, stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import { ExportButton } from "./export-button";
import { auditUnavailable, EXPORT_YAML, settingsRoutes } from "./fixtures";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

// jsdom has no object URLs: record the blobs instead.
const created: Blob[] = [];
const original = { create: URL.createObjectURL, revoke: URL.revokeObjectURL };

beforeEach(() => {
  created.length = 0;
  URL.createObjectURL = (b: Blob | MediaSource) => {
    created.push(b as Blob);
    return `blob:dash.test/${created.length}`;
  };
  URL.revokeObjectURL = () => {};
  vi.mocked(toast.error).mockClear();
});

afterEach(() => {
  URL.createObjectURL = original.create;
  URL.revokeObjectURL = original.revoke;
});

describe("ExportButton", () => {
  it("downloads the body of GET /settings/export as dash-settings.yaml", async () => {
    const f = stubFetch(settingsRoutes());
    const clicked: HTMLAnchorElement[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      clicked.push(this);
    });
    renderApp(<ExportButton />);
    await userEvent.setup().click(screen.getByRole("button", { name: /Export/ }));

    await waitFor(() => expect(clicked).toHaveLength(1));
    expect(f.callsTo(`GET ${settingsPaths.export}`)).toHaveLength(1);
    expect(clicked[0].download).toBe("dash-settings.yaml");
    expect(clicked[0].href).toBe("blob:dash.test/1");
    expect(await created[0].text()).toBe(EXPORT_YAML);
  });

  it("reports a refused export and downloads nothing", async () => {
    stubFetch(settingsRoutes({ [`GET ${settingsPaths.export}`]: () => auditUnavailable() }));
    const click = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});
    renderApp(<ExportButton />);
    await userEvent.setup().click(screen.getByRole("button", { name: /Export/ }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(click).not.toHaveBeenCalled();
    expect(created).toEqual([]);
  });
});
