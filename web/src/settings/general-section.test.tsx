import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderApp, stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import {
  envField,
  field,
  generalFields,
  invalidFields,
  patchResult,
  REVISION,
  settingsRoutes,
  settingsView,
  staleRevision,
} from "./fixtures";
import { GeneralSection } from "./general-section";
import { SettingsPage } from "./settings-page";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

beforeEach(() => {
  vi.mocked(toast.error).mockClear();
});

const ICON = "https://cdn.example.com/icons/";

describe("GeneralSection", () => {
  it("edits only ui.iconBaseURL; every other general field is read-only, locked or not", () => {
    stubFetch(settingsRoutes());
    const general = generalFields().map((f) =>
      f.path === "server.listen" ? { ...f, locked: false, lockedBy: undefined } : f,
    );
    renderApp(<GeneralSection view={settingsView({ general })} />);
    expect(screen.getAllByRole("textbox")).toEqual([screen.getByLabelText("ui.iconBaseURL")]);
    const listen = screen.getByText("server.listen").closest("div");
    if (!listen) throw new Error("no server.listen row");
    expect(within(listen).getByText("127.0.0.1:8080")).toBeInTheDocument();
    expect(within(listen).queryByRole("textbox")).toBeNull();
  });

  it("saves ui.iconBaseURL through PUT general with the revision, then reads the settings again", async () => {
    const f = stubFetch(settingsRoutes());
    const view = renderApp(<GeneralSection view={settingsView()} />);
    const invalidate = vi.spyOn(view.queryClient, "invalidateQueries");
    const u = userEvent.setup();
    await u.type(screen.getByLabelText("ui.iconBaseURL"), ICON);
    await u.keyboard("{Enter}");

    await waitFor(() => expect(f.callsTo(`PUT ${settingsPaths.general}`)).toHaveLength(1));
    const [put] = f.callsTo(`PUT ${settingsPaths.general}`);
    expect(put.body).toEqual({ ui: { iconBaseURL: ICON } });
    expect(put.headers.get("If-Match")).toBe(`"${REVISION}"`);
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ["settings"] }));
  });

  it("shows the server's problem under the field, described by it", async () => {
    stubFetch(
      settingsRoutes({
        [`PUT ${settingsPaths.general}`]: () =>
          invalidFields([{ path: "ui.iconBaseURL", message: "must be an http(s) URL" }]),
      }),
    );
    renderApp(<GeneralSection view={settingsView()} />);
    const u = userEvent.setup();
    const input = screen.getByLabelText("ui.iconBaseURL");
    await u.type(input, "ftp://x");
    await u.click(screen.getByRole("button", { name: "Save" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("must be an http(s) URL");
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(input).toHaveAccessibleDescription(/must be an http\(s\) URL/);
    expect(toast.error).not.toHaveBeenCalled();
  });

  // Pins that a failed save still refetches the settings: after a 412 the next attempt carries
  // the revision of the fresh read, not the stale one that was just refused.
  it("retries a save refused with 412 with the revision of the settings read again", async () => {
    const put = `PUT ${settingsPaths.general}`;
    const f = stubFetch(
      settingsRoutes({
        [put]: () => {
          f.route(`GET ${settingsPaths.view}`, settingsView({ revision: REVISION + 1 }));
          f.route(put, patchResult());
          return staleRevision();
        },
      }),
    );
    renderApp(<SettingsPage />);
    const u = userEvent.setup();
    await u.type(await screen.findByLabelText("ui.iconBaseURL"), `${ICON}{Enter}`);
    await waitFor(() => expect(f.callsTo(`GET ${settingsPaths.view}`)).toHaveLength(2));
    // The field remounts per revision, so the draft starts over at the fresh read.
    await waitFor(() => expect(screen.getByLabelText("ui.iconBaseURL")).toHaveValue(""));
    await u.type(screen.getByLabelText("ui.iconBaseURL"), `${ICON}{Enter}`);
    await waitFor(() => expect(f.callsTo(put)).toHaveLength(2));
    expect(f.callsTo(put).map((c) => c.headers.get("If-Match"))).toEqual([
      `"${REVISION}"`,
      `"${REVISION + 1}"`,
    ]);
  });

  it("shows ui.iconBaseURL read-only with its source when the environment sets it", () => {
    stubFetch(settingsRoutes());
    const general = generalFields().map((f) =>
      f.path === "ui.iconBaseURL"
        ? envField(f.path, "string", "DASH_UI_ICON_BASE_URL", { value: ICON, restart: true })
        : f,
    );
    renderApp(<GeneralSection view={settingsView({ general })} />);
    expect(screen.queryByRole("textbox")).toBeNull();
    expect(screen.getByText(ICON)).toBeInTheDocument();
    expect(screen.getByText(/env DASH_UI_ICON_BASE_URL/)).toBeInTheDocument();
  });

  it("shows auth.tokens as a summary per entry, never a hash", () => {
    stubFetch(settingsRoutes());
    const hash = `sha256:${"ab".repeat(32)}`;
    const tokens = field("auth.tokens", "array", {
      set: true,
      source: "file",
      value: [
        { name: "ci", role: "viewer", audience: "api", scopes: ["read", "logs"], hash },
        { name: "agent", role: "admin", audience: "mcp", scopes: ["read"], hash },
      ],
    });
    renderApp(<GeneralSection view={settingsView({ general: [...generalFields(), tokens] })} />);
    expect(
      screen.getByText("ci (viewer, api, read logs); agent (admin, mcp, read)"),
    ).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("sha256:");
  });
});
