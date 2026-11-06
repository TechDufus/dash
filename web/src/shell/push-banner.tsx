import { useQueryClient } from "@tanstack/react-query";
import { BellOff, X } from "lucide-react";
import { type JSX, useEffect, useReducer, useRef, useState } from "react";
import { Link } from "wouter";
import { pushAvailable, useMe } from "@/api/queries";
import { PushStateStale, PushStateVerified } from "@/api/types";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import {
  type DeviceSettings,
  invalidatePush,
  pushSupported,
  renewThisBrowser,
  resubscribeMarker,
  thisBrowserID,
  useSubscriptions,
} from "@/settings/devices-api";

/** The sessionStorage key that hides the banner for `user` for the rest of the browser session. */
export const pushBannerDismissKey = (user: string) => `dash:push-banner-dismissed:${user}`;

function readDismissed(user: string): boolean {
  try {
    return sessionStorage.getItem(pushBannerDismissKey(user)) === "1";
  } catch {
    return false;
  }
}

/**
 * Subscribes this browser to push again after a logout stopped it, once per page load, and says
 * so when it cannot. A logout turns every device of the user stale; the one this browser logged
 * out on was dropped and left its settings behind (resubscribeMarker). So: when this browser's
 * row of the signed-in user is stale, or the logout marker is there and the browser holds no
 * verified row, and the notification permission is still granted, dash subscribes it again with
 * that row's (or the marker's) name and notify settings and verifies it with the code the service
 * worker posts. Without the permission, or when a step fails, a dismissible banner links to the
 * Devices page.
 */
export function PushBanner(): JSX.Element | null {
  const me = useMe().data;
  const user = me?.user ?? "";
  const available = pushAvailable(me) && pushSupported();
  const id = available ? thisBrowserID(user) : null;
  const marker = available ? resubscribeMarker(user) : null;
  const subs = useSubscriptions({ kind: "own" }, id !== null);
  const row = id === null ? undefined : subs.data?.subscriptions.find((s) => s.id === id);
  const queryClient = useQueryClient();
  const [failed, setFailed] = useState(false);
  const [, dismissed] = useReducer((n: number) => n + 1, 0);

  // What to subscribe again with, once this browser's row (if it has one) is known.
  let need: { settings: DeviceSettings; replaces: string | null } | null = null;
  if (available && (id === null || subs.isSuccess)) {
    if (row?.state === PushStateStale) {
      need = { settings: { deviceName: row.deviceName, notify: row.notify }, replaces: row.id };
    } else if (marker !== null && row?.state !== PushStateVerified) {
      need = { settings: marker, replaces: row?.id ?? null };
    }
  }
  const needRef = useRef(need);
  needRef.current = need;
  const started = useRef(false);
  const needed = need !== null;

  useEffect(() => {
    const n = needRef.current;
    if (!needed || n === null || started.current) return;
    started.current = true;
    if (Notification.permission !== "granted") {
      setFailed(true);
      return;
    }
    renewThisBrowser(queryClient, user, n.settings, n.replaces)
      .catch(() => setFailed(true))
      .finally(() => void invalidatePush(queryClient));
  }, [needed, user, queryClient]);

  if (!failed || row?.state === PushStateVerified || readDismissed(user)) return null;
  const dismiss = () => {
    try {
      sessionStorage.setItem(pushBannerDismissKey(user), "1");
    } catch {
      // Storage refused: the banner stays hidden until this page reloads.
      setFailed(false);
    }
    dismissed();
  };
  return (
    <Alert role="status" className="mx-4 mt-3 w-auto pr-12 md:mx-6">
      <BellOff aria-hidden="true" />
      <AlertDescription>
        <p>
          Notifications on this device stopped when you logged out.{" "}
          <Link
            href="/settings/devices"
            className="font-medium text-accent underline-offset-4 hover:underline"
          >
            Subscribe again
          </Link>
        </p>
      </AlertDescription>
      <Button
        size="icon-sm"
        variant="ghost"
        aria-label="Dismiss"
        className="absolute top-2 right-2"
        onClick={dismiss}
      >
        <X aria-hidden="true" />
      </Button>
    </Alert>
  );
}
