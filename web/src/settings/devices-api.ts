import { type QueryClient, useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import {
  PushAttentionCrit,
  PushDeviceNameMaxLen,
  PushKindWebPush,
  type PushNotify,
  type PushSubscriptionInfo,
  type PushSubscriptionList,
  type PushSubscriptionRequest,
  type PushVAPID,
} from "@/api/types";

/** The paths of /api/v1/push (P7.7). */
export const pushPaths = {
  vapid: "/api/v1/push/vapid",
  list: "/api/v1/push/subscriptions",
  subscription: (id: string) => `/api/v1/push/subscriptions/${encodeURIComponent(id)}`,
  verify: (id: string) => `/api/v1/push/subscriptions/${encodeURIComponent(id)}/verify`,
  test: (id: string) => `/api/v1/push/subscriptions/${encodeURIComponent(id)}/test`,
};

/** Query keys: invalidating `pushKeys.all` refetches the list and the VAPID key. */
export const pushKeys = {
  all: ["push"] as const,
  list: ["push", "subscriptions"] as const,
  vapid: ["push", "vapid"] as const,
};

/** What a new subscription receives: every approval, and critical attention items. */
export const DEFAULT_NOTIFY: PushNotify = { approvals: true, attention: PushAttentionCrit };

/** The type of the message the service worker (web/sw/sw.js) posts with a verify push's code. */
export const VERIFY_MESSAGE = "dash-push-verify";

/** GET /api/v1/push/subscriptions; never requested while `enabled` is false. */
export function useSubscriptions(enabled: boolean) {
  return useQuery({
    queryKey: pushKeys.list,
    queryFn: () => api.get<PushSubscriptionList>(pushPaths.list),
    enabled,
  });
}

/**
 * GET /api/v1/push/vapid, read when the page opens so that Subscribe calls the browser without
 * waiting on the network first. The key changes only when dash generates a new pair.
 */
export function useVapid(enabled: boolean) {
  return useQuery({
    queryKey: pushKeys.vapid,
    queryFn: () => api.get<PushVAPID>(pushPaths.vapid),
    enabled,
    staleTime: Number.POSITIVE_INFINITY,
  });
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

/** DELETE /api/v1/push/subscriptions/{id}. */
export function deleteSubscription(id: string): Promise<void> {
  return api.del<void>(pushPaths.subscription(id));
}

/** After any write: the list is stale. */
export function invalidatePush(qc: QueryClient): Promise<void> {
  return qc.invalidateQueries({ queryKey: pushKeys.list });
}

// This browser's side. The ID of the subscription this browser registered is kept in
// localStorage: the list never shows an endpoint, so it is the only way to know which row is this
// browser's, and only this browser can re-POST it (the endpoint and keys are its own).
const THIS_BROWSER = "dash.push.subscription";

/** The ID of the subscription this browser registered, or null. */
export function thisBrowserID(): string | null {
  try {
    return window.localStorage.getItem(THIS_BROWSER);
  } catch {
    return null;
  }
}

/** Records (or with null forgets) the ID of this browser's subscription. */
export function setThisBrowserID(id: string | null): void {
  try {
    if (id === null) window.localStorage.removeItem(THIS_BROWSER);
    else window.localStorage.setItem(THIS_BROWSER, id);
  } catch {
    // storage refused (a private window): the row is just not marked as this browser's
  }
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
 * marks stale after a key change, is dropped first: a browser holds one per worker).
 */
export async function subscribeBrowser(publicKey: string): Promise<PushSubscription> {
  const reg = await navigator.serviceWorker.ready;
  const key = base64urlBytes(publicKey);
  const held = await reg.pushManager.getSubscription();
  if (held) {
    if (sameBytes(held.options.applicationServerKey, key)) return held;
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
