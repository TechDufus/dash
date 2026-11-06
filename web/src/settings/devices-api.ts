import { type QueryClient, useQuery } from "@tanstack/react-query";
import { ApiError, api } from "@/api/client";
import {
  ErrCodeConflict,
  PushAttentionCrit,
  PushDeviceNameMaxLen,
  PushKindWebPush,
  type PushNotify,
  PushStateVerified,
  type PushSubscriptionInfo,
  type PushSubscriptionList,
  type PushSubscriptionRequest,
  type PushVAPID,
} from "@/api/types";
import { type Owner, ownerParam } from "./owner-filter";

/** The paths of /api/v1/push (P7.7). */
export const pushPaths = {
  vapid: "/api/v1/push/vapid",
  list: "/api/v1/push/subscriptions",
  subscription: (id: string) => `/api/v1/push/subscriptions/${encodeURIComponent(id)}`,
  verify: (id: string) => `/api/v1/push/subscriptions/${encodeURIComponent(id)}/verify`,
  test: (id: string) => `/api/v1/push/subscriptions/${encodeURIComponent(id)}/test`,
};

/** Query keys: invalidating `pushKeys.all` refetches the lists and the VAPID key. */
export const pushKeys = {
  all: ["push"] as const,
  lists: ["push", "subscriptions"] as const,
  list: (user: string | undefined) => ["push", "subscriptions", user ?? ""] as const,
  vapid: ["push", "vapid"] as const,
};

/** What a new subscription receives: every approval, and critical attention items. */
export const DEFAULT_NOTIFY: PushNotify = { approvals: true, attention: PushAttentionCrit };

/** The type of the message the service worker (web/sw/sw.js) posts with a verify push's code. */
export const VERIFY_MESSAGE = "dash-push-verify";

/**
 * Calls `onCode` with the code of each verify message the service worker posts to this window;
 * returns the function that stops listening. Every window of dash gets each message.
 */
export function onVerifyCode(onCode: (code: string) => void): () => void {
  const onMessage = (e: MessageEvent) => {
    const d = e.data as { type?: unknown; code?: unknown } | null;
    if (d?.type === VERIFY_MESSAGE && typeof d.code === "string") onCode(d.code);
  };
  const container = navigator.serviceWorker;
  container.addEventListener("message", onMessage);
  return () => container.removeEventListener("message", onMessage);
}

/**
 * GET /api/v1/push/subscriptions, for `owner`: the caller's own, or (administrators) one user's
 * or everyone's; never requested while `enabled` is false.
 */
export function useSubscriptions(owner: Owner, enabled: boolean) {
  const user = ownerParam(owner);
  return useQuery({
    queryKey: pushKeys.list(user),
    queryFn: () => api.get<PushSubscriptionList>(pushPaths.list, { user }),
    enabled,
  });
}

const vapidQuery = {
  queryKey: pushKeys.vapid,
  queryFn: () => api.get<PushVAPID>(pushPaths.vapid),
  staleTime: Number.POSITIVE_INFINITY,
};

/**
 * GET /api/v1/push/vapid: the VAPID key, read when the page opens so that Subscribe calls the
 * browser without waiting on the network first, and the server URL the QR code holds. Both change
 * only with dash's configuration or a new key pair.
 */
export function useVapid(enabled: boolean) {
  return useQuery({ ...vapidQuery, enabled });
}

/**
 * POST /api/v1/push/subscriptions: registers a subscription, or replaces the caller's one with
 * the same endpoint (which is how its device name and notify settings change).
 */
export function registerSubscription(req: PushSubscriptionRequest): Promise<PushSubscriptionInfo> {
  return api.post<PushSubscriptionInfo>(pushPaths.list, req);
}

/** POST /api/v1/push/subscriptions/{id}/verify: five wrong codes delete the subscription. */
export function verifySubscription(id: string, code: string): Promise<unknown> {
  return api.post<unknown>(pushPaths.verify(id), { code });
}

/** POST /api/v1/push/subscriptions/{id}/test: at most one a minute per subscription. */
export function sendTestPush(id: string): Promise<unknown> {
  return api.post<unknown>(pushPaths.test(id));
}

/** DELETE /api/v1/push/subscriptions/{id}: one's own device, or (administrators) anyone's. */
export function deleteSubscription(id: string): Promise<void> {
  return api.del<void>(pushPaths.subscription(id));
}

/** After any write: every list is stale. */
export function invalidatePush(qc: QueryClient): Promise<void> {
  return qc.invalidateQueries({ queryKey: pushKeys.lists });
}

// This browser's side. The ID of the subscription this browser registered is kept in
// localStorage, under the user who registered it: the list never shows an endpoint, so it is the
// only way to know which row is this browser's, and only this browser can re-POST it (the
// endpoint and keys are its own). Several users may share one browser, which holds one push
// subscription: the key says whose it is.
function thisBrowserKey(user: string): string {
  return `dash.push.subscription:${user}`;
}

/** The ID of the subscription this browser registered for `user`, or null. */
export function thisBrowserID(user: string): string | null {
  try {
    return window.localStorage.getItem(thisBrowserKey(user));
  } catch {
    return null;
  }
}

/** Records (or with null forgets) the ID of this browser's subscription for `user`. */
export function setThisBrowserID(user: string, id: string | null): void {
  try {
    if (id === null) window.localStorage.removeItem(thisBrowserKey(user));
    else window.localStorage.setItem(thisBrowserKey(user), id);
  } catch {
    // storage refused (a private window): the row is just not marked as this browser's
  }
}

/** What a device registers with besides its endpoint: its name and its notify settings. */
export interface DeviceSettings {
  deviceName: string;
  notify: PushNotify;
}

// When `user` logs out, this browser's subscription is dropped (below), and its settings stay
// behind under the user's own key, without the ID or the endpoint: at that user's next sign-in in
// this browser, dash subscribes it again with them. Another user never reads them.
function resubscribeKey(user: string): string {
  return `dash.push.resubscribe:${user}`;
}

/** The settings of the subscription `user`'s logout dropped from this browser, or null. */
export function resubscribeMarker(user: string): DeviceSettings | null {
  let raw: string | null;
  try {
    raw = window.localStorage.getItem(resubscribeKey(user));
  } catch {
    return null;
  }
  if (raw === null) return null;
  try {
    const v = JSON.parse(raw) as Partial<DeviceSettings> | null;
    const n = v?.notify as Partial<PushNotify> | undefined;
    if (
      typeof v?.deviceName !== "string" ||
      v.deviceName.trim() === "" ||
      typeof n?.approvals !== "boolean" ||
      typeof n.attention !== "string"
    ) {
      return null;
    }
    return { deviceName: v.deviceName, notify: { approvals: n.approvals, attention: n.attention } };
  } catch {
    return null;
  }
}

/** Records (or with null forgets) the settings to subscribe this browser again with for `user`. */
export function setResubscribeMarker(user: string, settings: DeviceSettings | null): void {
  try {
    if (settings === null) window.localStorage.removeItem(resubscribeKey(user));
    else window.localStorage.setItem(resubscribeKey(user), JSON.stringify(settings));
  } catch {
    // storage refused (a private window): the next sign-in shows the banner instead
  }
}

/** Whether `err` is the 409 of an endpoint that another user of dash registered. */
export function isEndpointTaken(err: unknown): boolean {
  return err instanceof ApiError && err.status === 409 && err.code === ErrCodeConflict;
}

/** What the page says when this browser's endpoint belongs to another user. */
export const ENDPOINT_TAKEN =
  "This browser receives another account's notifications; remove it there, or use another browser profile.";

const DROP_TIMEOUT_MS = 3000;

/**
 * Before `user` logs out: when this browser's subscription is theirs, it is removed from dash and
 * dropped from the browser, so the next user of this browser gets none of their notifications;
 * its name and notify settings are kept for `user`'s next sign-in (resubscribeMarker). Best effort
 * and bounded, so that logging out never waits on it: dash marks the subscription stale once the
 * session ends anyway.
 */
export async function dropThisBrowser(user: string): Promise<void> {
  const id = thisBrowserID(user);
  if (id === null) return;
  setThisBrowserID(user, null);
  const drop = async () => {
    const list = await api.get<PushSubscriptionList>(pushPaths.list).catch(() => null);
    const row = list?.subscriptions.find((s) => s.id === id);
    if (row) setResubscribeMarker(user, { deviceName: row.deviceName, notify: row.notify });
    await deleteSubscription(id).catch(() => undefined);
    if (!pushSupported()) return;
    // getRegistration, not ready: ready never settles where no worker is registered.
    const reg = await navigator.serviceWorker.getRegistration();
    await (await reg?.pushManager.getSubscription())?.unsubscribe();
  };
  // The executor form: tsconfig's lib predates Promise.withResolvers (es2024).
  const timeout = new Promise<void>((resolve) => setTimeout(resolve, DROP_TIMEOUT_MS));
  await Promise.race([drop().catch(() => undefined), timeout]);
}

/** Whether this browser can subscribe to push: a service worker and PushManager in a secure context. */
export function pushSupported(): boolean {
  return (
    window.isSecureContext &&
    "serviceWorker" in navigator &&
    "PushManager" in window &&
    "Notification" in window
  );
}

/** The bytes of unpadded base64url text (the VAPID public key). */
export function base64urlBytes(text: string): Uint8Array<ArrayBuffer> {
  const b64 = text.replace(/-/g, "+").replace(/_/g, "/");
  const bin = atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4));
  const out = new Uint8Array(new ArrayBuffer(bin.length));
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function sameBytes(a: ArrayBuffer | null, b: Uint8Array): boolean {
  if (a === null || a.byteLength !== b.length) return false;
  const x = new Uint8Array(a);
  return x.every((v, i) => v === b[i]);
}

/**
 * This browser's push subscription for the VAPID key `publicKey`: the one it holds when that was
 * made with the same key, otherwise a new one (a subscription made with another key, which dash
 * marks stale after a key change, is dropped first: a browser holds one per worker). `fresh`
 * drops the held one whatever its key.
 */
export async function subscribeBrowser(
  publicKey: string,
  fresh = false,
): Promise<PushSubscription> {
  const reg = await navigator.serviceWorker.ready;
  const key = base64urlBytes(publicKey);
  const held = await reg.pushManager.getSubscription();
  if (held) {
    if (!fresh && sameBytes(held.options.applicationServerKey, key)) return held;
    await held.unsubscribe();
  }
  return reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
}

/** This browser's push subscription, or null; null too where push is not supported. */
export async function browserSubscription(): Promise<PushSubscription | null> {
  if (!pushSupported()) return null;
  const reg = await navigator.serviceWorker.ready;
  return reg.pushManager.getSubscription();
}

/** The body that registers `sub` (or re-registers it with new settings). */
export function subscriptionRequest(
  sub: PushSubscription,
  deviceName: string,
  notify: PushNotify,
): PushSubscriptionRequest {
  const j = sub.toJSON();
  return {
    endpoint: j.endpoint ?? sub.endpoint,
    keys: { p256dh: j.keys?.p256dh ?? "", auth: j.keys?.auth ?? "" },
    kind: PushKindWebPush,
    deviceName,
    notify,
  };
}

/** How long a renewal waits for the verify push's code. */
const VERIFY_WAIT_MS = 60_000;

let renewing = false;

/**
 * Whether renewThisBrowser runs: it submits the verify code itself, so the Devices page leaves
 * the codes that arrive meanwhile alone.
 */
export function renewingThisBrowser(): boolean {
  return renewing;
}

/** The next verify code the service worker posts; rejects after `ms`. `stop` ends the wait. */
function nextVerifyCode(ms: number): { code: Promise<string>; stop: () => void } {
  let stop = () => {};
  // The executor form: tsconfig's lib predates Promise.withResolvers (es2024).
  const code = new Promise<string>((resolve, reject) => {
    const timer = setTimeout(() => {
      off();
      reject(new Error("no verify code arrived"));
    }, ms);
    const off = onVerifyCode((c) => {
      clearTimeout(timer);
      off();
      resolve(c);
    });
    stop = () => {
      clearTimeout(timer);
      off();
    };
  });
  // Awaited only once the registration answered: a timeout before that is not unhandled.
  code.catch(() => undefined);
  return { code, stop };
}

/**
 * Subscribes this browser again for `user` without a user gesture (the notification permission
 * must be granted already): drops the browser's push subscription, makes a new one with dash's
 * current VAPID key, registers it with `settings`, and verifies it with the code the service
 * worker posts. `replaces`, the row this browser registered before, is removed once the new one
 * is registered under another ID: its endpoint went with the old subscription. The logout marker
 * is cleared once the device is verified. Rejects at the first step that fails.
 */
export async function renewThisBrowser(
  qc: QueryClient,
  user: string,
  settings: DeviceSettings,
  replaces: string | null,
): Promise<void> {
  renewing = true;
  // Listening before the POST: dash pushes the code as soon as it registered the device.
  const wait = nextVerifyCode(VERIFY_WAIT_MS);
  try {
    const { publicKey } = await qc.fetchQuery(vapidQuery);
    const sub = await subscribeBrowser(publicKey, true);
    const info = await registerSubscription(
      subscriptionRequest(sub, settings.deviceName, settings.notify),
    );
    setThisBrowserID(user, info.id);
    if (replaces !== null && replaces !== info.id) {
      await deleteSubscription(replaces).catch(() => undefined);
    }
    if (info.state !== PushStateVerified) await verifySubscription(info.id, await wait.code);
    setResubscribeMarker(user, null);
  } finally {
    wait.stop();
    renewing = false;
  }
}

/** A name for this browser, such as "Firefox on Android", to prefill the device name. */
export function defaultDeviceName(ua: string): string {
  const browser = /Edg\//.test(ua)
    ? "Edge"
    : /Firefox\//.test(ua)
      ? "Firefox"
      : /Chrom(e|ium)\//.test(ua)
        ? "Chrome"
        : /Safari\//.test(ua)
          ? "Safari"
          : "Browser";
  const os = /Android/.test(ua)
    ? "Android"
    : /iPhone/.test(ua)
      ? "iPhone"
      : /iPad/.test(ua)
        ? "iPad"
        : /Mac OS X/.test(ua)
          ? "macOS"
          : /Windows/.test(ua)
            ? "Windows"
            : /Linux/.test(ua)
              ? "Linux"
              : "";
  return (os ? `${browser} on ${os}` : browser).slice(0, PushDeviceNameMaxLen);
}
