import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import { renderApp, stubFetch } from "@/test/harness";
import { settingsPaths } from "./api";
import { patchResult, proxmoxView, REVISION, settingsRoutes } from "./fixtures";
import { PluginSettingsPage } from "./plugin-settings-page";

const CANARY = "canary-secret-7Hq2";
const SECRET_PUT = `PUT ${settingsPaths.secret("proxmox", "tokenSecret")}`;

/** The fixture install, whose GET answers the next revision once a write landed. */
function backend() {
  let revision = REVISION;
  const bump = () => {
    revision += 1;
    return patchResult({ revision });
  };
  return stubFetch(
    settingsRoutes({
      [`GET ${settingsPaths.plugin("proxmox")}`]: () => proxmoxView({ revision }),
      [SECRET_PUT]: bump,
      [`DELETE ${settingsPaths.secret("proxmox", "tokenSecret")}`]: bump,
    }),
  );
}

async function replaceWith(value: string) {
  const user = userEvent.setup();
  await user.click(await screen.findByRole("button", { name: "Replace tokenSecret" }));
  await user.type(screen.getByLabelText("New tokenSecret"), value);
  return user;
}

describe("TestSecretInputNeverInTheDOMAfterSave", () => {
  it("leaves the value in no text and no password input once saved", async () => {
    const stub = backend();
    const { container } = renderApp(<PluginSettingsPage id="proxmox" />);
    const user = await replaceWith(CANARY);
    expect(container.querySelector("input[type=password]")).toHaveValue(CANARY);

    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Saved."));

    expect(stub.callsTo(SECRET_PUT).map((c) => [c.headers.get("If-Match"), c.body])).toEqual([
      [`"${REVISION}"`, { value: CANARY }],
    ]);
    expect(container.textContent).not.toContain(CANARY);
    expect(container.querySelectorAll("input[type=password]")).toHaveLength(0);
    for (const input of container.querySelectorAll("input, textarea")) {
      expect((input as HTMLInputElement).value).not.toContain(CANARY);
    }
  });
});

describe("secret field", () => {
  it("sends a typed k8s: reference as a ref, never as a value", async () => {
    const stub = backend();
    renderApp(<PluginSettingsPage id="proxmox" />);
    const user = await replaceWith("k8s:dash/pve-token#secret");
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Saved."));
    expect(stub.callsTo(SECRET_PUT).map((c) => c.body)).toEqual([
      { ref: "k8s:dash/pve-token#secret" },
    ]);
  });

  // Pins the two-step Clear: the first press only asks, Cancel sends nothing, and the confirm
  // (by keyboard) sends the DELETE with the view's revision.
  it("Clear asks for confirmation, then deletes the stored secret with the view's revision", async () => {
    const stub = backend();
    const DEL = `DELETE ${settingsPaths.secret("proxmox", "tokenSecret")}`;
    renderApp(<PluginSettingsPage id="proxmox" />);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Clear tokenSecret" }));
    expect(stub.callsTo(DEL)).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "Cancel clearing tokenSecret" }));
    expect(stub.callsTo(DEL)).toHaveLength(0);
    expect(screen.getByRole("button", { name: "Clear tokenSecret" })).toHaveFocus();

    await user.keyboard("{Enter}");
    const confirm = screen.getByRole("button", { name: "Confirm clear tokenSecret" });
    expect(confirm).toHaveFocus();
    expect(stub.callsTo(DEL)).toHaveLength(0);
    await user.keyboard("{Enter}");
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Secret cleared."));
    expect(stub.callsTo(DEL).map((c) => c.headers.get("If-Match"))).toEqual([`"${REVISION}"`]);
    expect(stub.callsTo(SECRET_PUT)).toHaveLength(0);
  });
});
