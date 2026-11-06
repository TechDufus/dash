import { useQueryClient } from "@tanstack/react-query";
import { BellRing, Loader2, QrCode as QrIcon, Send, Trash2 } from "lucide-react";
import { type FormEvent, type JSX, useCallback, useEffect, useReducer, useState } from "react";
import { toast } from "sonner";
import { Link } from "wouter";
import { errorMessage } from "@/api/client";
import { pushAvailable, useMe } from "@/api/queries";
import {
  type Me,
  PushAttentionCrit,
  PushAttentionOff,
  PushAttentionWarn,
  PushCodeLen,
  PushDeviceNameMaxLen,
  PushKindUnifiedPush,
  type PushNotify,
  PushStatePending,
  PushStateStale,
  PushStateVerified,
  type PushSubscriptionInfo,
} from "@/api/types";
import { Page } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { cn } from "@/lib/utils";
import {
  browserSubscription,
  DEFAULT_NOTIFY,
  defaultDeviceName,
  deleteSubscription,
  ENDPOINT_TAKEN,
  invalidatePush,
  isEndpointTaken,
  pushSupported,
  registerSubscription,
  sendTestPush,
  setThisBrowserID,
  subscribeBrowser,
  subscriptionRequest,
  thisBrowserID,
  useSubscriptions,
  useVapid,
  VERIFY_MESSAGE,
  verifySubscription,
} from "./devices-api";
import { QrCode } from "./qr-code";

const ATTENTION: { value: string; label: string }[] = [
  { value: PushAttentionCrit, label: "Critical" },
  { value: PushAttentionWarn, label: "Warnings" },
  { value: PushAttentionOff, label: "Off" },
];

/**
 * /settings/devices (P7.7): the caller's push devices, subscribing this browser, and the QR code
 * the Android app scans. Open to every user where push is available (capabilities.push); anywhere
 * else the page says so and reads nothing.
 *
 * Subscribing: the button (a user gesture) asks for the notification permission, subscribes the
 * browser with dash's VAPID key and registers the subscription with approvals on and attention
 * at critical. dash then pushes a code; the service worker shows it and posts it to this page,
 * which submits it. The code field of a pending device is the fallback when no message comes.
 */
export function DevicesPage(): JSX.Element {
  const me = useMe();
  const available = pushAvailable(me.data);
  const supported = available && pushSupported();
  const subs = useSubscriptions(available);
  const vapid = useVapid(available);
  const queryClient = useQueryClient();
  // Whose this browser's subscription is: localStorage, read on every render under the user's
  // own key; `stored` re-renders the page after a write.
  const user = me.data?.user ?? "";
  const [, stored] = useReducer((n: number) => n + 1, 0);
  const mine = available ? thisBrowserID(user) : null;
  // The subscription this page just registered, which waits for its code.
  const [awaiting, setAwaiting] = useState<string | null>(null);
  // The last code the service worker posted that was not submitted yet.
  const [received, setReceived] = useState<string | null>(null);
  const [removing, setRemoving] = useState<PushSubscriptionInfo | null>(null);

  const list = subs.data?.subscriptions ?? [];
  const mineRow = list.find((s) => s.id === mine);

  const submitCode = useCallback(
    async (id: string, code: string): Promise<boolean> => {
      try {
        await verifySubscription(id, code);
        toast.success("Device verified: it receives notifications now");
        return true;
      } catch (err) {
        toast.error(`Verifying failed: ${errorMessage(err)}`);
        return false;
      } finally {
        void invalidatePush(queryClient);
      }
    },
    [queryClient],
  );

  // The service worker posts a verify push's code to every window of dash.
  useEffect(() => {
    if (!supported) return;
    const onMessage = (e: MessageEvent) => {
      const d = e.data as { type?: unknown; code?: unknown } | null;
      if (d?.type === VERIFY_MESSAGE && typeof d.code === "string") setReceived(d.code);
    };
    const container = navigator.serviceWorker;
    container.addEventListener("message", onMessage);
    return () => container.removeEventListener("message", onMessage);
  }, [supported]);

  // A code goes to the subscription this page registered, or else to this browser's pending one.
  const target = awaiting ?? (mineRow?.state === PushStatePending ? mineRow.id : null);
  useEffect(() => {
    if (target === null || received === null) return;
    setReceived(null);
    setAwaiting(null);
    void submitCode(target, received);
  }, [target, received, submitCode]);

  if (me.isPending) {
    return (
      <Page>
        <PanelSkeleton lines={4} />
      </Page>
    );
  }
  if (!available) {
    return (
      <Page>
        <h1 className="text-lg font-semibold text-fg">Devices</h1>
        <Panel>
          <Empty>
            <div role="status" className="space-y-3">
              <Unavailable me={me.data} />
            </div>
          </Empty>
        </Panel>
      </Page>
    );
  }

  function registered(info: PushSubscriptionInfo) {
    setThisBrowserID(user, info.id);
    stored();
    if (info.state !== PushStateVerified) setAwaiting(info.id);
  }

  return (
    <Page>
      <h1 className="text-lg font-semibold text-fg">Devices</h1>
      <SubscribePanel
        supported={supported}
        publicKey={vapid.data?.publicKey}
        keyError={vapid.isError ? errorMessage(vapid.error) : null}
        mineRow={mineRow}
        onRegistered={registered}
      />
      <Panel>
        <PanelHeader title="Your devices" icon={<BellRing />} />
        {subs.isPending ? (
          <PanelSkeleton lines={3} className="border-0 shadow-none" />
        ) : subs.isError ? (
          <Empty>Could not read the devices: {errorMessage(subs.error)}</Empty>
        ) : list.length === 0 ? (
          <Empty>No devices yet</Empty>
        ) : (
          <ul className="divide-y divide-line">
            {list.map((s) => (
              <DeviceRow
                key={s.id}
                sub={s}
                mine={s.id === mine}
                onVerify={submitCode}
                onRemove={() => setRemoving(s)}
              />
            ))}
          </ul>
        )}
      </Panel>
      {/* Drawn once /push/vapid answered, so the code never shows another address first; the
          address the page was opened at stands in when the answer has no serverUrl. */}
      {vapid.isPending ? null : <AppPanel url={vapid.data?.serverUrl || window.location.origin} />}
      <RemoveDialog
        sub={removing}
        mine={removing !== null && removing.id === mine}
        onClose={() => setRemoving(null)}
        onRemovedMine={() => {
          setThisBrowserID(user, null);
          stored();
        }}
      />
    </Page>
  );
}

/** Why this dash offers no push devices to `me`. */
function Unavailable({ me }: { me: Me | undefined }): JSX.Element {
  if (me?.authMode === "local" && me.role === "viewer") {
    return (
      <>
        <p>Sign in to manage your devices.</p>
        <Link href="/login" className="text-accent underline-offset-4 hover:underline">
          Sign in
        </Link>
      </>
    );
  }
  return (
    <>
      <p>
        Push notifications are not available on this dash: they need a writable data directory
        (dataDir), DASH_SECRET_KEY and a contact address for the push services (push.contact, or an
        https server.publicURL), in the local, oidc or proxy mode.
      </p>
      <Link href="/" className="text-accent underline-offset-4 hover:underline">
        Back to Overview
      </Link>
    </>
  );
}

/** The iOS needs: Web Push reaches only a home-screen app, from iOS 16.4. */
function IOSHint(): JSX.Element {
  return (
    <p className="text-sm text-muted">
      On iPhone and iPad, push needs iOS 16.4 or later and dash added to the Home Screen: in Safari,
      Share → Add to Home Screen, then open dash from its Home Screen icon and subscribe there.
    </p>
  );
}

/**
 * Subscribing this browser: a device name, then the button. Once the browser is subscribed the
 * panel says so instead; only a stale subscription (dash's push key changed) offers the button
 * again, as "Subscribe again".
 */
function SubscribePanel({
  supported,
  publicKey,
  keyError,
  mineRow,
  onRegistered,
}: {
  supported: boolean;
  publicKey: string | undefined;
  keyError: string | null;
  mineRow: PushSubscriptionInfo | undefined;
  onRegistered: (info: PushSubscriptionInfo) => void;
}): JSX.Element {
  const stale = mineRow?.state === PushStateStale;
  const notify = mineRow?.notify ?? DEFAULT_NOTIFY;
  const queryClient = useQueryClient();
  const [name, setName] = useState(() => defaultDeviceName(navigator.userAgent));
  const [busy, setBusy] = useState(false);
  const trimmed = name.trim();

  async function subscribe(e: FormEvent) {
    e.preventDefault();
    if (!publicKey || trimmed === "" || busy) return;
    setBusy(true);
    try {
      // First, while the click still counts as a user gesture: the permission prompt.
      const permission = await Notification.requestPermission();
      if (permission !== "granted") {
        toast.error(
          "Notifications are blocked for dash in this browser: allow them in the site settings, then try again.",
        );
        return;
      }
      const sub = await subscribeBrowser(publicKey);
      const info = await registerSubscription(subscriptionRequest(sub, trimmed, notify));
      onRegistered(info);
      toast.success(
        info.state === PushStateVerified
          ? `${info.deviceName} is subscribed`
          : `${info.deviceName} is subscribed: waiting for its code`,
      );
    } catch (err) {
      toast.error(
        isEndpointTaken(err) ? ENDPOINT_TAKEN : `Subscribing failed: ${errorMessage(err)}`,
      );
    } finally {
      setBusy(false);
      void invalidatePush(queryClient);
    }
  }

  return (
    <Panel>
      <PanelHeader title="This browser" icon={<BellRing />} />
      <div className="space-y-3 px-4 pb-4">
        {!supported ? (
          <p role="status" className="text-sm text-fg">
            This browser cannot receive push notifications here.
          </p>
        ) : mineRow && !stale ? (
          <p role="status" className="text-sm text-fg">
            This browser is subscribed as <span className="font-medium">{mineRow.deviceName}</span>
            {mineRow.state === PushStatePending ? ": waiting for the code dash pushed to it." : "."}{" "}
            Remove it below to stop its notifications.
          </p>
        ) : (
          <form onSubmit={subscribe} className="flex flex-wrap items-end gap-2">
            <div className="grid gap-1">
              <Label htmlFor="device-name">Device name</Label>
              <Input
                id="device-name"
                className="h-8 w-64"
                autoComplete="off"
                maxLength={PushDeviceNameMaxLen}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <Button type="submit" size="sm" disabled={!publicKey || trimmed === "" || busy}>
              {busy ? <Loader2 className="animate-spin" /> : <BellRing aria-hidden="true" />}
              {stale ? "Subscribe again" : "Subscribe this browser"}
            </Button>
          </form>
        )}
        {keyError ? (
          <p className="text-sm text-crit">Could not read dash's push key: {keyError}</p>
        ) : null}
        {mineRow && !stale ? null : <IOSHint />}
      </div>
    </Panel>
  );
}

/** One device: what it is, its state, its notify settings and what can be done with it. */
function DeviceRow({
  sub,
  mine,
  onVerify,
  onRemove,
}: {
  sub: PushSubscriptionInfo;
  mine: boolean;
  onVerify: (id: string, code: string) => Promise<boolean>;
  onRemove: () => void;
}): JSX.Element {
  const [testing, setTesting] = useState(false);

  async function test() {
    setTesting(true);
    try {
      await sendTestPush(sub.id);
      toast.success(`Test push queued for ${sub.deviceName}: it shows there shortly`);
    } catch (err) {
      toast.error(`The test push failed: ${errorMessage(err)}`);
    } finally {
      setTesting(false);
    }
  }

  return (
    <li className="space-y-2 px-4 py-3" data-subscription={sub.id}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="font-medium text-fg">{sub.deviceName}</span>
        {mine ? <Badge variant="secondary">this browser</Badge> : null}
        <StateBadge state={sub.state} />
        <span className="text-xs text-muted">
          {sub.kind === PushKindUnifiedPush ? "UnifiedPush" : "Web Push"} ·{" "}
          <span className="num">{sub.endpointHost}</span>
        </span>
        <div className="ml-auto flex gap-1">
          {sub.state === PushStateVerified ? (
            <Button size="sm" variant="outline" disabled={testing} onClick={() => void test()}>
              {testing ? <Loader2 className="animate-spin" /> : <Send aria-hidden="true" />}
              Send test push
            </Button>
          ) : null}
          <Button
            size="sm"
            variant="ghost"
            aria-label={`Remove ${sub.deviceName}`}
            onClick={onRemove}
          >
            <Trash2 aria-hidden="true" />
            Remove
          </Button>
        </div>
      </div>
      {sub.state === PushStateStale ? (
        <p className="text-sm text-warn">
          dash's push key changed, so this device receives nothing.{" "}
          {mine ? "Subscribe this browser again above." : "Subscribe again on that device."}
        </p>
      ) : null}
      {sub.state === PushStatePending ? <CodeForm sub={sub} onVerify={onVerify} /> : null}
      {sub.state === PushStateStale ? (
        // A stale subscription was made with dash's old key: a change would re-register it with
        // that key, and the new code could never reach it. Subscribing again is the way back.
        mine ? null : (
          <NotifySummary notify={sub.notify} />
        )
      ) : mine ? (
        <NotifyEditor sub={sub} />
      ) : (
        <NotifySummary notify={sub.notify} />
      )}
    </li>
  );
}

function StateBadge({ state }: { state: string }): JSX.Element {
  if (state === PushStateVerified) return <Badge variant="outline">verified</Badge>;
  if (state === PushStateStale) return <Badge variant="destructive">stale</Badge>;
  return <Badge variant="secondary">pending</Badge>;
}

/** The manual fallback for a pending device: the code of its verify notification. */
function CodeForm({
  sub,
  onVerify,
}: {
  sub: PushSubscriptionInfo;
  onVerify: (id: string, code: string) => Promise<boolean>;
}): JSX.Element {
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (code.trim() === "" || busy) return;
    setBusy(true);
    if (await onVerify(sub.id, code.trim())) setCode("");
    setBusy(false);
  }

  return (
    <form onSubmit={submit} className="flex flex-wrap items-center gap-2">
      <span className="text-sm text-muted">
        Waiting for the code dash pushed to this device. If it does not arrive by itself, type the
        code from the notification:
      </span>
      <Input
        aria-label={`Code for ${sub.deviceName}`}
        autoComplete="one-time-code"
        className="num h-8 w-36"
        maxLength={PushCodeLen + 4}
        value={code}
        onChange={(e) => setCode(e.target.value)}
      />
      <Button type="submit" size="sm" variant="outline" disabled={code.trim() === "" || busy}>
        {busy && <Loader2 className="animate-spin" />}
        Verify
      </Button>
    </form>
  );
}

/** Another device's settings: only that device can change them (it holds the endpoint). */
function NotifySummary({ notify }: { notify: PushNotify }): JSX.Element {
  return (
    <p className="text-sm text-muted">
      Approvals {notify.approvals ? "on" : "off"} · attention:{" "}
      {ATTENTION.find((a) => a.value === notify.attention)?.label ?? notify.attention}. Change this
      on the device itself.
    </p>
  );
}

/**
 * This browser's notify settings. A change registers the browser's subscription again with the
 * new settings: the same endpoint replaces the row, and it stays verified.
 */
function NotifyEditor({ sub }: { sub: PushSubscriptionInfo }): JSX.Element {
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);

  async function update(notify: PushNotify) {
    setBusy(true);
    try {
      const held = await browserSubscription();
      if (!held) {
        toast.error("This browser no longer holds its subscription: subscribe it again.");
        return;
      }
      await registerSubscription(subscriptionRequest(held, sub.deviceName, notify));
      toast.success(`Saved the notifications of ${sub.deviceName}`);
    } catch (err) {
      toast.error(`Saving failed: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
      void invalidatePush(queryClient);
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
      <div className="flex items-center gap-2">
        <Switch
          id={`approvals-${sub.id}`}
          checked={sub.notify.approvals}
          disabled={busy}
          onCheckedChange={(on) => void update({ ...sub.notify, approvals: on })}
        />
        <Label htmlFor={`approvals-${sub.id}`}>Approvals</Label>
      </div>
      <div className="flex items-center gap-2">
        <span className="text-sm text-muted">Attention</span>
        {/* biome-ignore lint/a11y/useSemanticElements: a group of toggle buttons, not a form fieldset */}
        <div role="group" aria-label="Attention" className="flex gap-1">
          {ATTENTION.map((a) => {
            const on = sub.notify.attention === a.value;
            return (
              <Button
                key={a.value}
                size="sm"
                variant={on ? "secondary" : "ghost"}
                aria-pressed={on}
                disabled={busy}
                className={cn(on && "pointer-events-none")}
                onClick={() => void update({ ...sub.notify, attention: a.value })}
              >
                {a.label}
              </Button>
            );
          })}
        </div>
      </div>
    </div>
  );
}

/** The QR code the Android app scans: dash's address, nothing else (D47). */
function AppPanel({ url }: { url: string }): JSX.Element {
  return (
    <Panel>
      <PanelHeader title="The Android app" icon={<QrIcon />} />
      <div className="flex flex-wrap items-center gap-4 px-4 pb-4">
        <QrCode value={url} className="size-44 rounded-sm" />
        <div className="space-y-1 text-sm">
          <p className="text-fg">Scan this code in the dash app to fill in the server address.</p>
          <p className="text-muted">
            The dash Android app is not released yet: it comes in a later release.
          </p>
          <p className="num text-muted">{url}</p>
          <p className="text-muted">It holds the address only, never a credential.</p>
        </div>
      </div>
    </Panel>
  );
}

/** Asks before DELETE …/{id}; for this browser's row it also drops the browser's subscription. */
function RemoveDialog({
  sub,
  mine,
  onClose,
  onRemovedMine,
}: {
  sub: PushSubscriptionInfo | null;
  mine: boolean;
  onClose: () => void;
  onRemovedMine: () => void;
}): JSX.Element {
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  return (
    <AlertDialog open={!!sub} onOpenChange={(o) => !o && !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Remove {sub?.deviceName}?</AlertDialogTitle>
          <AlertDialogDescription>
            It receives no more notifications from dash. Subscribe it again to get them back.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Keep</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={busy}
            onClick={async (e) => {
              e.preventDefault();
              if (!sub) return;
              setBusy(true);
              try {
                await deleteSubscription(sub.id);
                if (mine) {
                  onRemovedMine();
                  await (await browserSubscription().catch(() => null))?.unsubscribe();
                }
                toast.success(`Removed ${sub.deviceName}`);
                onClose();
              } catch (err) {
                toast.error(`Removing failed: ${errorMessage(err)}`);
              } finally {
                setBusy(false);
                void invalidatePush(queryClient);
              }
            }}
          >
            {busy && <Loader2 className="animate-spin" />}
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
