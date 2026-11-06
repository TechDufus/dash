import { act, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { toast } from "sonner";
import { afterEach, beforeEach, describe, expect, it, type Mock, vi } from "vitest";
import type { Me, PushSubscriptionInfo } from "@/api/types";
import { base64urlBytes } from "@/settings/devices-api";
import { DevicesPage } from "@/settings/devices-page";
import { me } from "@/settings/fixtures";
import { json, renderApp, stubFetch } from "@/test/harness";
import { PushBanner, pushBannerDismissKey } from "./push-banner";

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

// After a logout dash turns all of the user's devices stale. The shell subscribes this browser
// again by itself where the notification permission still holds, and otherwise shows a banner.

const VAPID =
  "BNcRdreALRFXTkOOUHK1EtK2wtaz5Ry4YfYCA_0QTpQtUbVlUls0VJXg7A8u-Ts1XbjhazAkj7I99e8QcYP7DkM";
const ENDPOINT = "https://push.example.com/send/abc123";
const P256DH = "BPdeviceKey";
const AUTH = "authSecret";
const LIST = "GET /api/v1/push/subscriptions";
const REGISTER = "POST /api/v1/push/subscriptions";
const ID = "sub_ABCDEFGHIJKLMNOPQRSTUVWXYZ";
const NEW_ID = "sub_NEWNEWNEWNEWNEWNEWNEWNEWNE";
const MINE_KEY = "dash.push.subscription:alice";
const MARKER_KEY = "dash.push.resubscribe:alice";
const BANNER = "Notifications on this device stopped when you logged out.";

function sub(over: Partial<PushSubscriptionInfo> = {}): PushSubscriptionInfo {
  return {
    id: ID,
    user: "alice",
    kind: "webpush",
    deviceName: "Laptop",
    notify: { approvals: false, attention: "warn" },
    state: "verified",
    endpointHost: "push.example.com",
    createdAt: "2026-11-01T10:00:00Z",
    verifiedAt: "2026-11-01T10:00:05Z",
    ...over,
  };
}

/** alice, a viewer in the proxy mode, on a dash with push. */
function alice(): Me {
  const base = me("viewer");
  return { ...base, capabilities: { ...base.capabilities, push: true } };
}

function routes(over: Record<string, unknown> = {}) {
  return {
    "GET /api/v1/me": alice(),
    "GET /api/v1/push/vapid": { publicKey: VAPID, serverUrl: "https://dash.example.com" },
    [LIST]: { subscriptions: [sub({ state: "stale" })] },
    [REGISTER]: () => json(sub({ state: "pending", verifiedAt: null }), 201),
    [`POST /api/v1/push/subscriptions/${ID}/verify`]: () => sub(),
    [`POST /api/v1/push/subscriptions/${NEW_ID}/verify`]: () => sub({ id: NEW_ID }),
    [`DELETE /api/v1/push/subscriptions/${ID}`]: undefined,
    ...over,
  };
}

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

function stubPermission(permission: NotificationPermission) {
  vi.stubGlobal("Notification", { permission, requestPermission: vi.fn() });
}

/** The service worker posting a verify push's code to every window. */
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
  vi.mocked(toast.error).mockClear();
  container = Object.assign(new EventTarget(), { ready: Promise.resolve({ pushManager }) });
  Object.defineProperty(navigator, "serviceWorker", { value: container, configurable: true });
  vi.stubGlobal("isSecureContext", true);
  vi.stubGlobal("PushManager", class {});
  stubPermission("granted");
});

afterEach(() => {
  Reflect.deleteProperty(navigator, "serviceWorker");
});

describe("PushBanner, subscribing this browser again after a logout", () => {
  it("re-registers a stale row with its own settings: unsubscribe, subscribe, POST, verify", async () => {
    localStorage.setItem(MINE_KEY, ID);
    // The subscription this browser held before the logout, with dash's current key.
    const old = fakeSubscription(base64urlBytes(VAPID));
    held = old;
    const stub = stubFetch(routes());
    renderApp(<PushBanner />);

    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    expect(old.unsubscribe).toHaveBeenCalledTimes(1);
    expect(pushManager.subscribe).toHaveBeenCalledExactlyOnceWith({
      userVisibleOnly: true,
      applicationServerKey: base64urlBytes(VAPID),
    });
    expect(stub.callsTo(REGISTER)[0]?.body).toEqual({
      endpoint: ENDPOINT,
      keys: { p256dh: P256DH, auth: AUTH },
      kind: "webpush",
      deviceName: "Laptop",
      notify: { approvals: false, attention: "warn" },
    });

    postCode("ABCD2345");
    const VERIFY = `POST /api/v1/push/subscriptions/${ID}/verify`;
    await waitFor(() => expect(stub.callsTo(VERIFY)).toHaveLength(1));
    expect(stub.callsTo(VERIFY)[0]?.body).toEqual({ code: "ABCD2345" });
    // Re-registered under the same ID: nothing to remove.
    expect(stub.callsTo(`DELETE /api/v1/push/subscriptions/${ID}`)).toEqual([]);
    expect(localStorage.getItem(MINE_KEY)).toBe(ID);
    expect(screen.queryByText(BANNER)).not.toBeInTheDocument();
  });

  it("removes the stale row when the new subscription registers under another ID", async () => {
    localStorage.setItem(MINE_KEY, ID);
    const stub = stubFetch(
      routes({
        [REGISTER]: () => json(sub({ id: NEW_ID, state: "pending", verifiedAt: null }), 201),
      }),
    );
    renderApp(<PushBanner />);
    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    await waitFor(() =>
      expect(stub.callsTo(`DELETE /api/v1/push/subscriptions/${ID}`)).toHaveLength(1),
    );
    postCode("WXYZ6789");
    await waitFor(() =>
      expect(stub.callsTo(`POST /api/v1/push/subscriptions/${NEW_ID}/verify`)).toHaveLength(1),
    );
    expect(localStorage.getItem(MINE_KEY)).toBe(NEW_ID);
  });

  it("subscribes again from the logout marker at the same user's next sign-in, then clears it", async () => {
    // alice logged out in this browser: its row was removed, its settings kept.
    localStorage.setItem(
      MARKER_KEY,
      JSON.stringify({ deviceName: "Laptop", notify: { approvals: false, attention: "warn" } }),
    );
    const stub = stubFetch(
      routes({
        [REGISTER]: () => json(sub({ id: NEW_ID, state: "pending", verifiedAt: null }), 201),
      }),
    );
    renderApp(<PushBanner />);
    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    expect(stub.callsTo(REGISTER)[0]?.body).toMatchObject({
      deviceName: "Laptop",
      notify: { approvals: false, attention: "warn" },
    });
    // The marker stays until the device is verified.
    expect(localStorage.getItem(MARKER_KEY)).not.toBeNull();

    postCode("ABCD2345");
    await waitFor(() => expect(localStorage.getItem(MARKER_KEY)).toBeNull());
    expect(stub.callsTo(`POST /api/v1/push/subscriptions/${NEW_ID}/verify`)[0]?.body).toEqual({
      code: "ABCD2345",
    });
    expect(localStorage.getItem(MINE_KEY)).toBe(NEW_ID);
    expect(stub.callsTo(`DELETE /api/v1/push/subscriptions/${ID}`)).toEqual([]);
  });

  it("never acts on another user's marker", async () => {
    localStorage.setItem(
      "dash.push.resubscribe:bob",
      JSON.stringify({
        deviceName: "Bob's laptop",
        notify: { approvals: true, attention: "crit" },
      }),
    );
    const stub = stubFetch(routes());
    renderApp(<PushBanner />);
    await waitFor(() => expect(stub.callsTo("GET /api/v1/me")).toHaveLength(1));
    await new Promise((r) => setTimeout(r, 50));
    expect(stub.callsTo(REGISTER)).toEqual([]);
    expect(stub.callsTo(LIST)).toEqual([]);
    expect(pushManager.subscribe).not.toHaveBeenCalled();
    expect(screen.queryByText(BANNER)).not.toBeInTheDocument();
  });

  it("leaves a verified row alone", async () => {
    localStorage.setItem(MINE_KEY, ID);
    const stub = stubFetch(routes({ [LIST]: { subscriptions: [sub()] } }));
    renderApp(<PushBanner />);
    await waitFor(() => expect(stub.callsTo(LIST)).toHaveLength(1));
    await new Promise((r) => setTimeout(r, 50));
    expect(stub.callsTo(REGISTER)).toEqual([]);
    expect(screen.queryByText(BANNER)).not.toBeInTheDocument();
  });

  it("shows the banner when re-registering fails, and dismissing hides it", async () => {
    const user = userEvent.setup();
    localStorage.setItem(MINE_KEY, ID);
    stubFetch(routes({ [REGISTER]: () => json({ error: "boom", code: "internal" }, 500) }));
    const { unmount } = renderApp(<PushBanner />);

    expect(await screen.findByText(BANNER)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Subscribe again" })).toHaveAttribute(
      "href",
      "/settings/devices",
    );
    await user.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText(BANNER)).not.toBeInTheDocument();
    expect(sessionStorage.getItem(pushBannerDismissKey("alice"))).toBe("1");

    // Another page load in the same browser session tries again, and stays quiet on failure.
    unmount();
    const stub = stubFetch(
      routes({ [REGISTER]: () => json({ error: "boom", code: "internal" }, 500) }),
    );
    renderApp(<PushBanner />);
    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    await new Promise((r) => setTimeout(r, 50));
    expect(screen.queryByText(BANNER)).not.toBeInTheDocument();
  });

  it("shows the banner, and subscribes nothing, without the notification permission", async () => {
    stubPermission("default");
    localStorage.setItem(MINE_KEY, ID);
    const stub = stubFetch(routes());
    renderApp(<PushBanner />);
    expect(await screen.findByText(BANNER)).toBeInTheDocument();
    expect(pushManager.subscribe).not.toHaveBeenCalled();
    expect(stub.callsTo(REGISTER)).toEqual([]);
  });

  it("verifies once when the Devices page is open: the page leaves the renewal's code alone", async () => {
    localStorage.setItem(MINE_KEY, ID);
    let state = "stale";
    const stub = stubFetch(
      routes({
        // The list follows the server: pending once the browser registered again, then verified.
        [LIST]: () => ({ subscriptions: [sub({ state })] }),
        [REGISTER]: () => {
          state = "pending";
          return json(sub({ state: "pending", verifiedAt: null }), 201);
        },
        [`POST /api/v1/push/subscriptions/${ID}/verify`]: () => {
          state = "verified";
          return sub();
        },
      }),
    );
    const { queryClient } = renderApp(
      <>
        <PushBanner />
        <DevicesPage />
      </>,
    );
    await waitFor(() => expect(stub.callsTo(REGISTER)).toHaveLength(1));
    // The page reads its list again and sees this browser's row pending, the row it would
    // otherwise submit a posted code to.
    await act(() => queryClient.invalidateQueries());
    expect(await screen.findByLabelText("Code for Laptop")).toBeInTheDocument();
    postCode("ABCD2345");
    const VERIFY = `POST /api/v1/push/subscriptions/${ID}/verify`;
    await waitFor(() => expect(stub.callsTo(VERIFY)).toHaveLength(1));
    await new Promise((r) => setTimeout(r, 50));
    expect(stub.callsTo(VERIFY)).toHaveLength(1);
    expect(toast.error).not.toHaveBeenCalled();
  });
});
