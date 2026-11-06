import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import type * as Uqr from "uqr";
import { encode } from "uqr";
import { afterEach, beforeEach, describe, expect, it, type Mock, vi } from "vitest";
import type { Me, PushSubscriptionInfo } from "@/api/types";
import { CORE_PAGES } from "@/plugins/registry";
import { type FetchCall, json, renderApp, stubFetch } from "@/test/harness";
import { base64urlBytes } from "./devices-api";
import { DevicesPage } from "./devices-page";
import { me } from "./fixtures";
import { UserSettingsPage } from "./settings-nav";

// The QR test checks what the page encodes: encode stays the real one, wrapped in a spy.
vi.mock("uqr", async (importOriginal) => {
  const real = await importOriginal<typeof Uqr>();
  return { ...real, encode: vi.fn(real.encode) };
});

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const VAPID =
  "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM";
const SERVER_URL = "https://dash.example.com";
const ENDPOINT = "https://push.example.com/send/abc123";
const P256DH = "BPdeviceKey";
const AUTH = "authSecret";
const LIST = "GET /api/v1/push/subscriptions";
const REGISTER = "POST /api/v1/push/subscriptions";
const ID = "sub_ABCDEFGHIJKLMNOPQRSTUVWXYZ";
/** alice's own key for this browser's subscription. */
const MINE_KEY = "dash.push.subscription:alice";

function sub(over: Partial<PushSubscriptionInfo> = {}): PushSubscriptionInfo {
  return {
    id: ID,
    user: "alice",
    kind: "webpush",
    deviceName: "Laptop",
    notify: { approvals: true, attention: "crit" },
    state: "verified",
    endpointHost: "push.example.com",
    createdAt: "2026-11-01T10:00:00Z",
    verifiedAt: "2026-11-01T10:00:05Z",
    ...over,
  };
}

/** alice, a viewer in the proxy mode, on a dash with push. */
function meWith(push: boolean, over: Partial<Me> = {}): Me {
  const base = me("viewer");
  return { ...base, capabilities: { ...base.capabilities, push }, ...over };
}

function routes(who: Me, over: Record<string, unknown> = {}) {
  return {
    "GET /api/v1/me": who,
    "GET /api/v1/push/vapid": { publicKey: VAPID, serverUrl: SERVER_URL },
    [LIST]: { subscriptions: [] },
    [REGISTER]: () => json(sub({ state: "pending", verifiedAt: null }), 201),
    [`POST /api/v1/push/subscriptions/${ID}/verify`]: () => sub(),
    [`POST /api/v1/push/subscriptions/${ID}/test`]: undefined,
    [`DELETE /api/v1/push/subscriptions/${ID}`]: undefined,
    ...over,
  };
}

// The browser's push side: a service worker container that dispatches messages, a PushManager
// holding at most one subscription, and the notification permission.
interface FakeSubscription {
  endpoint: string;
  options: { applicationServerKey: ArrayBuffer | null };
  toJSON: () => PushSubscriptionJSON;
  unsubscribe: Mock;
}
let held: FakeSubscription | null;
let container: EventTarget & { ready: Promise<unknown> };
const pushManager = {
  getSubscription: vi.fn(async () => held),
  subscribe: vi.fn(async (opts: PushSubscriptionOptionsInit) => {
    held = fakeSubscription(opts.applicationServerKey as Uint8Array);
    return held;
  }),
};
const requestPermission = vi.fn(async () => "granted");

function fakeSubscription(key: Uint8Array): FakeSubscription {
  return {
    endpoint: ENDPOINT,
    options: { applicationServerKey: key.slice().buffer },
    toJSON: () => ({ endpoint: ENDPOINT, keys: { p256dh: P256DH, auth: AUTH } }),
    unsubscribe: vi.fn(async () => {
      held = null;
      return true;
    }),
  };
}

/** The service worker posting a verify push's code to the page. */
function postCode(code: string) {
  act(() => {
    container.dispatchEvent(
      new MessageEvent("message", { data: { type: "dash-push-verify", code } }),
    );
  });
}

beforeEach(() => {
  held = null;
  pushManager.getSubscription.mockClear();
  pushManager.subscribe.mockClear();
  requestPermission.mockClear();
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
  container = Object.assign(new EventTarget(), { ready: Promise.resolve({ pushManager }) });
  Object.defineProperty(navigator, "serviceWorker", { value: container, configurable: true });
  vi.stubGlobal("isSecureContext", true);
  vi.stubGlobal("PushManager", class {});
  vi.stubGlobal("Notification", { requestPermission });
});

afterEach(() => {
  Reflect.deleteProperty(navigator, "serviceWorker");
});

describe("DevicesPage", () => {
  it("subscribes this browser, then submits the code the service worker posts", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes(meWith(true)));
    renderApp(<DevicesPage />);
    const name = await screen.findByLabelText("Device name");
    await user.clear(name);
    await user.type(name, "Laptop");
    const button = screen.getByRole("button", { name: "Subscribe this browser" });
    await waitFor(() => expect(button).toBeEnabled()); // the VAPID key is read
    await user.click(button);

    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    expect(requestPermission).toHaveBeenCalledTimes(1);
    expect(pushManager.subscribe).toHaveBeenCalledExactlyOnceWith({
      userVisibleOnly: true,
      applicationServerKey: base64urlBytes(VAPID),
    });
    const [post] = stub.callsTo(REGISTER);
    expect(post?.body).toEqual({
      endpoint: ENDPOINT,
      keys: { p256dh: P256DH, auth: AUTH },
      kind: "webpush",
      deviceName: "Laptop",
      notify: { approvals: true, attention: "crit" },
    });
    expect(post?.headers.get("X-Dash-Csrf")).toBe("1");
    expect(stub.callsTo(`POST /api/v1/push/subscriptions/${ID}/verify`)).toEqual([]);

    postCode("ABCD2345");
    await waitFor(() =>
      expect(stub.callsTo(`POST /api/v1/push/subscriptions/${ID}/verify`)).toHaveLength(1),
    );
    expect(stub.callsTo(`POST /api/v1/push/subscriptions/${ID}/verify`)[0]?.body).toEqual({
      code: "ABCD2345",
    });
    expect(localStorage.getItem(MINE_KEY)).toBe(ID);
  });

  it("marks no row as this browser's when the stored ID is another user's", async () => {
    // bob subscribed this browser earlier; alice is signed in now.
    localStorage.setItem("dash.push.subscription:bob", ID);
    stubFetch(routes(meWith(true), { [LIST]: { subscriptions: [sub()] } }));
    renderApp(<DevicesPage />);
    const row = (await screen.findByText("Laptop")).closest("li") as HTMLElement;
    expect(within(row).queryByText("this browser")).not.toBeInTheDocument();
    expect(within(row).queryByRole("switch")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Subscribe this browser" })).toBeInTheDocument();
  });

  it("says the endpoint is another account's on the 409 conflict, and stores nothing", async () => {
    const user = userEvent.setup();
    stubFetch(
      routes(meWith(true), {
        [REGISTER]: () =>
          json({ error: "another user registered this push endpoint", code: "conflict" }, 409),
      }),
    );
    renderApp(<DevicesPage />);
    const button = await screen.findByRole("button", { name: "Subscribe this browser" });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "This browser receives another account's notifications; remove it there, or use another browser profile.",
      ),
    );
    expect(localStorage.getItem(MINE_KEY)).toBeNull();
  });

  it("keeps the plain failure for the other 409s", async () => {
    const user = userEvent.setup();
    stubFetch(
      routes(meWith(true), {
        [REGISTER]: () => json({ error: "at most 10 devices", code: "push_limit" }, 409),
      }),
    );
    renderApp(<DevicesPage />);
    const button = await screen.findByRole("button", { name: "Subscribe this browser" });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1));
    expect(vi.mocked(toast.error).mock.calls[0]?.[0]).toMatch(/^Subscribing failed: /);
  });

  it("submits a code that arrives before the registration answers", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(
      routes(meWith(true), {
        [REGISTER]: () => {
          postCode("WXYZ6789"); // dash pushes the code while the POST is still in flight
          return json(sub({ state: "pending", verifiedAt: null }), 201);
        },
      }),
    );
    renderApp(<DevicesPage />);
    const button = await screen.findByRole("button", { name: "Subscribe this browser" });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);
    await waitFor(() =>
      expect(stub.callsTo(`POST /api/v1/push/subscriptions/${ID}/verify`)).toHaveLength(1),
    );
    expect(stub.callsTo(`POST /api/v1/push/subscriptions/${ID}/verify`)[0]?.body).toEqual({
      code: "WXYZ6789",
    });
  });

  it("ignores a service worker message of another type, or without a string code", async () => {
    localStorage.setItem(MINE_KEY, ID);
    const stub = stubFetch(
      routes(meWith(true), {
        [LIST]: { subscriptions: [sub({ state: "pending", verifiedAt: null })] },
      }),
    );
    renderApp(<DevicesPage />);
    await screen.findByLabelText("Code for Laptop"); // this browser's pending row is listed
    const VERIFY = `POST /api/v1/push/subscriptions/${ID}/verify`;
    const post = (data: unknown) =>
      act(() => {
        container.dispatchEvent(new MessageEvent("message", { data }));
      });

    post({ type: "dash-push-test", code: "ABCD2345" });
    post({ type: "dash-push-verify-x", code: "ABCD2345" });
    post({ code: "ABCD2345" });
    post({ type: "dash-push-verify", code: 12345678 });
    post("dash-push-verify");
    post(null);
    // Nothing went out: a verify would be sent right away.
    await new Promise((r) => setTimeout(r, 50));
    expect(stub.callsTo(VERIFY)).toEqual([]);

    // The listener is live: the right message goes through.
    postCode("ABCD2345");
    await waitFor(() => expect(stub.callsTo(VERIFY)).toHaveLength(1));
    expect(stub.callsTo(VERIFY)[0]?.body).toEqual({ code: "ABCD2345" });
  });

  it("registers nothing when the notification permission is refused", async () => {
    const user = userEvent.setup();
    requestPermission.mockResolvedValueOnce("denied");
    const stub = stubFetch(routes(meWith(true)));
    renderApp(<DevicesPage />);
    const button = await screen.findByRole("button", { name: "Subscribe this browser" });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);
    await waitFor(() => expect(requestPermission).toHaveBeenCalledTimes(1));
    expect(pushManager.subscribe).not.toHaveBeenCalled();
    expect(stub.callsTo(REGISTER)).toEqual([]);
  });

  it("verifies a pending device with the code typed in, the fallback", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(
      routes(meWith(true), {
        [LIST]: { subscriptions: [sub({ state: "pending", verifiedAt: null })] },
      }),
    );
    renderApp(<DevicesPage />);
    await user.type(await screen.findByLabelText("Code for Laptop"), "abcd-2345");
    await user.click(screen.getByRole("button", { name: "Verify" }));
    await waitFor(() =>
      expect(stub.callsTo(`POST /api/v1/push/subscriptions/${ID}/verify`)).toHaveLength(1),
    );
    expect(stub.callsTo(`POST /api/v1/push/subscriptions/${ID}/verify`)[0]?.body).toEqual({
      code: "abcd-2345",
    });
  });

  it("lists each device with its kind, name, push service host and state", async () => {
    stubFetch(
      routes(meWith(true), {
        [LIST]: {
          subscriptions: [
            sub(),
            sub({
              id: "sub_PHONE",
              kind: "unifiedpush",
              deviceName: "Pixel",
              endpointHost: "ntfy.example.com",
              notify: { approvals: false, attention: "warn" },
            }),
            sub({ id: "sub_OLD", deviceName: "Old tablet", state: "stale" }),
          ],
        },
      }),
    );
    renderApp(<DevicesPage />);
    const pixel = await screen.findByText("Pixel");
    const row = pixel.closest("li") as HTMLElement;
    expect(within(row).getByText("ntfy.example.com")).toBeInTheDocument();
    expect(within(row).getByText(/UnifiedPush/)).toBeInTheDocument();
    expect(within(row).getByText("verified")).toBeInTheDocument();
    expect(
      within(row).getByText(
        /Approvals off · attention:\s*Warnings\. Change this on the device itself\./,
      ),
    ).toBeInTheDocument();
    expect(within(row).queryByRole("switch")).not.toBeInTheDocument();
    expect(within(row).getByRole("button", { name: "Remove Pixel" })).toBeInTheDocument();
    const old = screen.getByText("Old tablet").closest("li") as HTMLElement;
    expect(within(old).getByText("stale")).toBeInTheDocument();
    expect(within(old).getByText(/Subscribe again on that device/)).toBeInTheDocument();
    expect(within(old).queryByRole("button", { name: "Send test push" })).not.toBeInTheDocument();
    // The endpoint itself is never shown (the API never sends it).
    expect(document.body.textContent).not.toContain(ENDPOINT);
  });

  it("changes this browser's notify settings by registering its subscription again", async () => {
    const user = userEvent.setup();
    localStorage.setItem(MINE_KEY, ID);
    held = fakeSubscription(base64urlBytes(VAPID));
    const stub = stubFetch(
      routes(meWith(true), { [LIST]: { subscriptions: [sub()] }, [REGISTER]: () => sub() }),
    );
    renderApp(<DevicesPage />);
    const row = (await screen.findByText("this browser")).closest("li") as HTMLElement;
    await user.click(within(row).getByRole("button", { name: "Warnings" }));
    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    expect(stub.callsTo(REGISTER)[0]?.body).toEqual({
      endpoint: ENDPOINT,
      keys: { p256dh: P256DH, auth: AUTH },
      kind: "webpush",
      deviceName: "Laptop",
      notify: { approvals: true, attention: "warn" },
    });

    await user.click(within(row).getByRole("switch"));
    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(2));
    expect(stub.callsTo(REGISTER)[1]?.body).toMatchObject({
      endpoint: ENDPOINT,
      notify: { approvals: false, attention: "crit" },
    });
    expect(pushManager.subscribe).not.toHaveBeenCalled();
  });

  it("sends a test push to a verified device, and says it is queued", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes(meWith(true), { [LIST]: { subscriptions: [sub()] } }));
    renderApp(<DevicesPage />);
    await user.click(await screen.findByRole("button", { name: "Send test push" }));
    await waitFor(() =>
      expect(stub.callsTo(`POST /api/v1/push/subscriptions/${ID}/test`)).toHaveLength(1),
    );
    // 202: dash queued it; the push service has not taken it yet.
    await waitFor(() =>
      expect(toast.success).toHaveBeenCalledWith(
        expect.stringMatching(/^Test push queued for Laptop/),
      ),
    );
    expect(toast.success).not.toHaveBeenCalledWith(expect.stringMatching(/sent/));
  });

  it("says this browser is subscribed, and offers no Subscribe button then", async () => {
    localStorage.setItem(MINE_KEY, ID);
    held = fakeSubscription(base64urlBytes(VAPID));
    stubFetch(routes(meWith(true), { [LIST]: { subscriptions: [sub()] } }));
    renderApp(<DevicesPage />);
    expect(await screen.findByText(/This browser is subscribed as/)).toHaveTextContent(
      "This browser is subscribed as Laptop. Remove it below to stop its notifications.",
    );
    expect(screen.queryByRole("button", { name: /Subscribe/ })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Device name")).not.toBeInTheDocument();
  });

  it("says a pending browser waits for its code", async () => {
    localStorage.setItem(MINE_KEY, ID);
    stubFetch(
      routes(meWith(true), {
        [LIST]: { subscriptions: [sub({ state: "pending", verifiedAt: null })] },
      }),
    );
    renderApp(<DevicesPage />);
    expect(await screen.findByText(/This browser is subscribed as/)).toHaveTextContent(
      "This browser is subscribed as Laptop: waiting for the code dash pushed to it.",
    );
    expect(screen.queryByRole("button", { name: /Subscribe/ })).not.toBeInTheDocument();
  });

  it("offers only Subscribe again on this browser's stale row, which re-registers with the new key", async () => {
    const user = userEvent.setup();
    localStorage.setItem(MINE_KEY, ID);
    // The browser's subscription was made with dash's previous key.
    const old = fakeSubscription(new Uint8Array([4, 1, 2, 3]));
    held = old;
    const stub = stubFetch(
      routes(meWith(true), {
        [LIST]: { subscriptions: [sub({ state: "stale" })] },
      }),
    );
    renderApp(<DevicesPage />);
    const row = (await screen.findByText("this browser")).closest("li") as HTMLElement;
    expect(within(row).getByText("stale")).toBeInTheDocument();
    // No notify editor: a change would re-register the old-key subscription, and its code
    // could never arrive.
    expect(within(row).queryByRole("switch")).not.toBeInTheDocument();
    expect(within(row).queryByRole("group", { name: "Attention" })).not.toBeInTheDocument();
    expect(within(row).queryByRole("button", { name: "Warnings" })).not.toBeInTheDocument();
    expect(within(row).queryByRole("button", { name: "Send test push" })).not.toBeInTheDocument();
    expect(within(row).getByText(/Subscribe this browser again above/)).toHaveTextContent(
      "dash's push key changed, you logged out (logging out stops push on your browsers), or the dash app was disconnected, so this device receives nothing. Subscribe this browser again above.",
    );
    expect(screen.queryByText(/This browser is subscribed as/)).not.toBeInTheDocument();

    const again = screen.getByRole("button", { name: "Subscribe again" });
    await waitFor(() => expect(again).toBeEnabled());
    await user.click(again);
    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    expect(old.unsubscribe).toHaveBeenCalledTimes(1);
    expect(pushManager.subscribe).toHaveBeenCalledExactlyOnceWith({
      userVisibleOnly: true,
      applicationServerKey: base64urlBytes(VAPID),
    });
  });

  it("prefills Subscribe again with the stale row's device name, and registers with it", async () => {
    const user = userEvent.setup();
    localStorage.setItem(MINE_KEY, ID);
    const stub = stubFetch(
      routes(meWith(true), {
        [LIST]: {
          subscriptions: [
            sub({
              state: "stale",
              deviceName: "Work laptop",
              notify: { approvals: false, attention: "warn" },
            }),
          ],
        },
      }),
    );
    renderApp(<DevicesPage />);
    const again = await screen.findByRole("button", { name: "Subscribe again" });
    expect(screen.getByLabelText("Device name")).toHaveValue("Work laptop");
    await waitFor(() => expect(again).toBeEnabled());
    await user.click(again);
    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    expect(stub.callsTo(REGISTER)[0]?.body).toMatchObject({
      deviceName: "Work laptop",
      notify: { approvals: false, attention: "warn" },
    });
  });

  it("forgets the logout marker once this browser subscribes again by hand", async () => {
    const user = userEvent.setup();
    const MARKER = "dash.push.resubscribe:alice";
    localStorage.setItem(
      MARKER,
      JSON.stringify({ deviceName: "Laptop", notify: { approvals: true, attention: "crit" } }),
    );
    const stub = stubFetch(routes(meWith(true)));
    renderApp(<DevicesPage />);
    const button = await screen.findByRole("button", { name: "Subscribe this browser" });
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);
    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    await waitFor(() => expect(localStorage.getItem(MARKER)).toBeNull());
    expect(localStorage.getItem(MINE_KEY)).toBe(ID);
  });

  it("asks before removing a device, and deletes only after the confirmation", async () => {
    const user = userEvent.setup();
    localStorage.setItem(MINE_KEY, ID);
    held = fakeSubscription(base64urlBytes(VAPID));
    const unsubscribe = held.unsubscribe;
    const stub = stubFetch(routes(meWith(true), { [LIST]: { subscriptions: [sub()] } }));
    renderApp(<DevicesPage />);
    const DELETE = `DELETE /api/v1/push/subscriptions/${ID}`;

    await user.click(await screen.findByRole("button", { name: "Remove Laptop" }));
    let dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Remove Laptop?")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Keep" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(stub.callsTo(DELETE)).toEqual([]);

    await user.click(screen.getByRole("button", { name: "Remove Laptop" }));
    dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(stub.callsTo(DELETE)).toHaveLength(1));
    // This browser's own row: the browser's subscription goes too.
    await waitFor(() => expect(unsubscribe).toHaveBeenCalledTimes(1));
    expect(localStorage.getItem(MINE_KEY)).toBeNull();
  });

  /** The QR code's SVG once it shows `url`, checked to draw exactly the matrix of `url`. */
  async function qrOf(url: string) {
    const svg = await screen.findByRole("img", { name: `QR code of ${url}` });
    const want = encode(url, { ecc: "M", border: 4 });
    const drawn = new Set(
      [...svg.querySelectorAll("g > rect")].map(
        (r) => `${r.getAttribute("x")},${r.getAttribute("y")}`,
      ),
    );
    const dark = new Set<string>();
    want.data.forEach((row, y) => {
      row.forEach((on, x) => {
        if (on) dark.add(`${x},${y}`);
      });
    });
    expect(drawn).toEqual(dark);
    expect(svg.getAttribute("viewBox")).toBe(`0 0 ${want.size} ${want.size}`);
  }

  it("encodes dash's server URL, and only that, in the QR code", async () => {
    vi.mocked(encode).mockClear();
    stubFetch(routes(meWith(true)));
    renderApp(<DevicesPage />);
    await qrOf(SERVER_URL);
    expect(screen.getByText(SERVER_URL)).toBeInTheDocument();
    expect(vi.mocked(encode)).toHaveBeenCalled();
    // Every QR the page drew holds the server URL: none was drawn before /push/vapid answered.
    for (const [input] of vi.mocked(encode).mock.calls.slice(0, -1)) expect(input).toBe(SERVER_URL);
  });

  it("encodes the address dash was opened at when the answer has no server URL", async () => {
    vi.mocked(encode).mockClear();
    stubFetch(routes(meWith(true), { "GET /api/v1/push/vapid": { publicKey: VAPID } }));
    renderApp(<DevicesPage />);
    await qrOf(window.location.origin);
    for (const [input] of vi.mocked(encode).mock.calls.slice(0, -1)) {
      expect(input).toBe(window.location.origin);
    }
  });

  it("shows the iOS home-screen hint", async () => {
    stubFetch(routes(meWith(true)));
    renderApp(<DevicesPage />);
    expect(
      await screen.findByText(/iOS 16\.4 or later and dash added to the Home Screen/),
    ).toBeInTheDocument();
  });

  it("says the Android app is not released yet before the scan instruction", async () => {
    stubFetch(routes(meWith(true)));
    renderApp(<DevicesPage />);
    await qrOf(SERVER_URL);
    const notYet = screen.getByText(/The dash Android app is not released yet/);
    const scan = screen.getByText(/Scan this code in the dash app/);
    expect(notYet.compareDocumentPosition(scan) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("says so where this browser cannot receive push, and still lists the devices", async () => {
    vi.stubGlobal("PushManager", undefined);
    Reflect.deleteProperty(window, "PushManager");
    stubFetch(routes(meWith(true), { [LIST]: { subscriptions: [sub()] } }));
    renderApp(<DevicesPage />);
    expect(
      await screen.findByText("This browser cannot receive push notifications here."),
    ).toBeInTheDocument();
    expect(await screen.findByText("Laptop")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Subscribe this browser" }),
    ).not.toBeInTheDocument();
    expect(pushManager.subscribe).not.toHaveBeenCalled();
    // The QR code does not need push in this browser.
    await qrOf(SERVER_URL);
  });

  it("is hidden without capabilities.push: no link to it, and the page reads nothing", async () => {
    const stub = stubFetch(routes(meWith(false)));
    renderApp(<DevicesPage />);
    expect(
      await screen.findByText(/Push notifications are not available on this dash/),
    ).toHaveTextContent(
      /writable data directory \(dataDir\), DASH_SECRET_KEY and a contact address for the push services \(push\.contact, or an https server\.publicURL\), in the local, oidc or proxy mode\./,
    );
    expect(stub.calls.filter((c) => c.path.startsWith("/api/v1/push"))).toEqual([]);

    const settings = CORE_PAGES.find((p) => p.key === "core:settings");
    expect(settings?.visibleTo?.(meWith(false))).toBe(false);
    expect(settings?.visibleTo?.(meWith(true))).toBe(true);
  });

  it("is listed among the user's settings pages only with capabilities.push", async () => {
    stubFetch(routes(meWith(true)));
    const { unmount } = renderApp(<UserSettingsPage />);
    expect(await screen.findByRole("link", { name: "Devices" })).toHaveAttribute(
      "href",
      "/settings/devices",
    );
    unmount();

    const tokensOnly = meWith(false);
    stubFetch(
      routes({ ...tokensOnly, capabilities: { ...tokensOnly.capabilities, tokens: true } }),
    );
    renderApp(<UserSettingsPage />);
    expect(await screen.findByRole("link", { name: "Tokens" })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Devices" })).not.toBeInTheDocument();
  });
});

describe("DevicesPage, an administrator's view of other users' devices", () => {
  const BOB = "sub_BOBPHONEBOBPHONEBOBPHONEBO";
  const bobPhone = sub({ id: BOB, user: "bob", deviceName: "Phone" });
  const bobPending = sub({
    id: "sub_BOBTABLETBOBTABLETBOBTABLE",
    user: "bob",
    deviceName: "Tablet",
    state: "pending",
    verifiedAt: null,
  });
  const bobStale = sub({
    id: "sub_BOBOLDBOBOLDBOBOLDBOBOLDBO",
    user: "bob",
    deviceName: "Old laptop",
    state: "stale",
  });

  /** The list per ?user=: alice's own, bob's, or everyone's. */
  function lists(call: FetchCall) {
    const user = call.query.get("user");
    const bobs = [bobPhone, bobPending, bobStale];
    if (user === "*") return { subscriptions: [sub(), ...bobs] };
    if (user === "bob") return { subscriptions: bobs };
    if (user === "nobody") return { subscriptions: [] };
    return { subscriptions: [sub()] };
  }

  it("gives a viewer no owner filter, and lists only their own", async () => {
    const stub = stubFetch(routes(meWith(true), { [LIST]: lists }));
    renderApp(<DevicesPage />);
    expect(await screen.findByText("Laptop")).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Whose devices" })).not.toBeInTheDocument();
    expect(stub.callsTo(LIST).every((c) => !c.query.has("user"))).toBe(true);
  });

  it("lists everyone's devices, read-only for others, and removes another user's", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(
      routes(meWith(true, { role: "admin" }), {
        [LIST]: lists,
        [`DELETE /api/v1/push/subscriptions/${BOB}`]: undefined,
      }),
    );
    renderApp(<DevicesPage />);
    expect(await screen.findByRole("heading", { name: "Your devices" })).toBeInTheDocument();
    const filter = await screen.findByRole("group", { name: "Whose devices" });
    await user.click(within(filter).getByRole("button", { name: "All users" }));

    expect(await screen.findByRole("heading", { name: "Everyone's devices" })).toBeInTheDocument();
    expect(stub.callsTo(LIST).some((c) => c.query.get("user") === "*")).toBe(true);
    const phone = (await screen.findByText("Phone")).closest("li") as HTMLElement;
    expect(within(phone).getByText("bob")).toBeInTheDocument();
    // Another user's device: settings read-only, no test push, only Remove.
    expect(within(phone).queryByRole("switch")).not.toBeInTheDocument();
    expect(
      within(phone).getByText(
        /Approvals on · attention:\s*Critical\.\s*Only bob changes this, on that device\./,
      ),
    ).toBeInTheDocument();
    expect(within(phone).queryByRole("button", { name: "Send test push" })).toBeNull();
    const tablet = screen.getByText("Tablet").closest("li") as HTMLElement;
    expect(within(tablet).queryByLabelText("Code for Tablet")).not.toBeInTheDocument();
    const old = screen.getByText("Old laptop").closest("li") as HTMLElement;
    expect(
      within(old).getByText(/It gets notifications again once bob subscribes it again\./),
    ).toBeInTheDocument();
    // The administrator's own device keeps its own actions.
    const laptop = screen.getByText("Laptop").closest("li") as HTMLElement;
    expect(within(laptop).getByRole("button", { name: "Send test push" })).toBeInTheDocument();

    await user.click(within(phone).getByRole("button", { name: "Remove Phone of bob" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Remove bob's Phone?")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() =>
      expect(stub.callsTo(`DELETE /api/v1/push/subscriptions/${BOB}`)).toHaveLength(1),
    );
    expect(toast.success).toHaveBeenCalledWith("Removed bob's Phone");
  });

  it("hides the owner filter in the local mode, whose one account is the administrator", async () => {
    const stub = stubFetch(
      routes(meWith(true, { role: "admin", authMode: "local" }), { [LIST]: lists }),
    );
    renderApp(<DevicesPage />);
    expect(await screen.findByText("Laptop")).toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Whose devices" })).not.toBeInTheDocument();
    expect(stub.callsTo(LIST).every((c) => !c.query.has("user"))).toBe(true);
  });

  it("hides the owner filter while no other user has a device", async () => {
    const stub = stubFetch(
      routes(meWith(true, { role: "admin" }), {
        [LIST]: (call: FetchCall) =>
          call.query.get("user") === "*" ? { subscriptions: [sub()] } : lists(call),
      }),
    );
    renderApp(<DevicesPage />);
    expect(await screen.findByText("Laptop")).toBeInTheDocument();
    await waitFor(() =>
      expect(stub.callsTo(LIST).some((c) => c.query.get("user") === "*")).toBe(true),
    );
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByRole("group", { name: "Whose devices" })).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Your devices" })).toBeInTheDocument();
  });

  it("lists one user's devices once a name is given", async () => {
    const user = userEvent.setup();
    const stub = stubFetch(routes(meWith(true, { role: "admin" }), { [LIST]: lists }));
    renderApp(<DevicesPage />);
    const filter = await screen.findByRole("group", { name: "Whose devices" });
    await user.click(within(filter).getByRole("button", { name: "One user" }));
    expect(screen.getByText("Type a user name, then Show.")).toBeInTheDocument();
    const before = stub.callsTo(LIST).length;
    // Besides one's own list, only everyone's (which shows the filter): no one user's yet.
    expect(stub.callsTo(LIST).some((c) => c.query.has("user") && c.query.get("user") !== "*")).toBe(
      false,
    );

    await user.type(screen.getByLabelText("User name"), "bob");
    await user.click(screen.getByRole("button", { name: "Show" }));
    expect(await screen.findByRole("heading", { name: "Devices of bob" })).toBeInTheDocument();
    expect(await screen.findByText("Phone")).toBeInTheDocument();
    expect(
      stub
        .callsTo(LIST)
        .slice(before)
        .map((c) => c.query.get("user")),
    ).toContain("bob");
    // This browser's card still follows the administrator's own devices.
    expect(screen.getByRole("button", { name: "Subscribe this browser" })).toBeInTheDocument();

    await user.clear(screen.getByLabelText("User name"));
    await user.type(screen.getByLabelText("User name"), "nobody");
    await user.click(screen.getByRole("button", { name: "Show" }));
    expect(await screen.findByText("No devices")).toBeInTheDocument();
  });
});
