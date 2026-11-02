import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ActionInfo, PluginInfo } from "@/api/types";
import { ui } from "@/api/ui-store";
import { connectStream, type TestStream } from "@/plugins/test-util";
import { type FetchStub, json, pluginInfo, renderApp, stubFetch } from "@/test/harness";
import { GlobalDialogs } from "./dialogs";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const RESTART = "POST /api/v1/plugins/kubernetes/actions/restart";

const restartAction = (): ActionInfo => ({
  name: "restart",
  title: "Restart",
  destructive: true,
  idempotent: false,
  confirm: "none",
  minRole: "admin",
  inputSchema: null,
});

const row = (actions: ActionInfo[] = [restartAction()]): PluginInfo =>
  pluginInfo({ id: "kubernetes", name: "Kubernetes", topics: ["kubernetes.cluster"], actions });

describe("the restart dialog", () => {
  let stream: TestStream;
  let stub: FetchStub;

  beforeEach(() => {
    vi.mocked(toast.success).mockClear();
    vi.mocked(toast.error).mockClear();
    stub = stubFetch({
      "GET /api/v1/me": { user: "u", role: "admin", scopes: ["read"], ui: { iconBaseUrl: "" } },
      [RESTART]: json({}, 202),
    });
    stream = connectStream();
  });
  afterEach(() => {
    act(() => ui.closeRestart());
    stream.close();
  });

  async function open(target: Parameters<typeof ui.openRestart>[0], rows = [row()]) {
    renderApp(<GlobalDialogs />);
    stream.plugins(rows);
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    await act(async () => {}); // /me has been read into the query cache
    act(() => ui.openRestart(target));
    return screen.findByRole("alertdialog");
  }

  const plain = { kind: "Deployment", namespace: "media", name: "plex", protected: false } as const;
  const guarded = { ...plain, namespace: "kube-system", name: "dns", protected: true } as const;

  it("posts the restart to the kubernetes plugin's action route and closes", async () => {
    await open(plain);
    await userEvent.click(screen.getByRole("button", { name: "Restart" }));
    await waitFor(() => expect(stub.callsTo(RESTART)).toHaveLength(1));
    expect(stub.callsTo(RESTART)[0]?.body).toEqual({
      kind: "Deployment",
      namespace: "media",
      name: "plex",
      confirm: "",
    });
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(toast.success).toHaveBeenCalledWith("Restart requested: Deployment media/plex");
  });

  it("asks for the workload's name in a protected namespace and sends it as the confirmation", async () => {
    await open(guarded);
    const go = screen.getByRole("button", { name: "Restart" });
    expect(go).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/to confirm/), "dn");
    expect(go).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/to confirm/), "s");
    expect(go).toBeEnabled();
    await userEvent.click(go);
    await waitFor(() => expect(stub.callsTo(RESTART)).toHaveLength(1));
    expect(stub.callsTo(RESTART)[0]?.body).toMatchObject({ name: "dns", confirm: "dns" });
  });

  it("stays open and shows the server's message when the action is refused", async () => {
    stub.route(
      RESTART,
      json({ error: "too many actions, try again later", code: "rate_limited" }, 429),
    );
    await open(plain);
    await userEvent.click(screen.getByRole("button", { name: "Restart" }));
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("too many actions, try again later"),
    );
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restart" })).toBeEnabled();
  });

  it("cannot be confirmed when the plugin does not list the action", async () => {
    await open(plain, [row([])]);
    expect(screen.getByText("Restart is not available right now.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled();
    expect(stub.callsTo(RESTART)).toHaveLength(0);
  });

  it("loses its confirmation when actions.enabled is switched off under it", async () => {
    await open(plain);
    expect(screen.getByRole("button", { name: "Restart" })).toBeEnabled();
    stream.plugins([row([])]);
    await waitFor(() => expect(screen.getByRole("button", { name: "Restart" })).toBeDisabled());
  });
});
