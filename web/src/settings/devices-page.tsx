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
  onVerifyCode,
  pushSupported,
  registerSubscription,
  renewingThisBrowser,
  sendTestPush,
  setResubscribeMarker,
  setThisBrowserID,
  subscribeBrowser,
  subscriptionRequest,
  thisBrowserID,
  useSubscriptions,
  useVapid,
  verifySubscription,
} from "./devices-api";
import { type Owner, OwnerFilter } from "./owner-filter";
import { QrCode } from "./qr-code";

const ATTENTION: { value: string; label: string }[] = [
  { value: PushAttentionCrit, label: "Critical" },
  { value: PushAttentionWarn, label: "Warnings" },
  { value: PushAttentionOff, label: "Off" },
];

const OWN: Owner = { kind: "own" };
const ALL: Owner = { kind: "all" };

/**
 * /settings/devices (P7.7): the caller's push devices, subscribing this browser, and the QR code
 * the Android app scans. Open to every user where push is available (capabilities.push); anywhere
 * else the page says so and reads nothing. An administrator also lists one user's or everyone's
 * devices (the owner filter, shown once another user has a device; never in the local mode, whose
 * one account is the administrator) and may remove them; another user's notify settings are shown
 * read-only, and only the owner tests or verifies a device.
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
  const admin = me.data?.role === "admin";
  const [owner, setOwner] = useState<Owner>(OWN);
  // The owner filter needs other users: the local mode has one account, the administrator; in
  // the others it shows once everyone's list holds a device of another user.
  const multiUser = admin && me.data?.authMode !== "local";
  const everyone = useSubscriptions(ALL, available && multiUser);
  const showFilter =
    multiUser && (everyone.data?.subscriptions.some((s) => s.user !== me.data?.user) ?? false);
  // Without the filter (a viewer, or nobody else to show) the list is one's own devices.
  const effective: Owner = showFilter ? owner : OWN;
  // "One user" before a name was typed lists nobody's devices.
  const awaitingName = effective.kind === "user" && effective.name === "";
  // The caller's own list knows this browser's row; the shown one is the owner filter's (the
  // same query when that is the caller's own).
  const subs = useSubscriptions(OWN, available);
  const shown = useSubscriptions(effective, available && !awaitingName);
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

  const mineRow = subs.data?.subscriptions.find((s) => s.id === mine);
  const list = shown.data?.subscriptions ?? [];

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

  // The service worker posts a verify push's code to every window of dash; while the shell
  // subscribes this browser again by itself (renewThisBrowser), that code is its own.
  useEffect(() => {
    if (!supported) return;
    return onVerifyCode((code) => {
      if (!renewingThisBrowser()) setReceived(code);
    });
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
    setResubscribeMarker(user, null);
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
        <PanelHeader title={listTitle(effective)} icon={<BellRing />}>
          {showFilter ? (
            <OwnerFilter label="Whose devices" owner={owner} onChange={setOwner} />
          ) : null}
        </PanelHeader>
        {awaitingName ? (
          <Empty>Type a user name, then Show.</Empty>
        ) : shown.isPending ? (
          <PanelSkeleton lines={3} className="border-0 shadow-none" />
        ) : shown.isError ? (
          <Empty>Could not read the devices: {errorMessage(shown.error)}</Empty>
        ) : list.length === 0 ? (
          <Empty>{effective.kind === "own" ? "No devices yet" : "No devices"}</Empty>
        ) : (
          <ul className="divide-y divide-line">
            {list.map((s) => (
              <DeviceRow
                key={s.id}
                sub={s}
                mine={s.id === mine && s.user === user}
                other={s.user !== user}
                showUser={effective.kind !== "own"}
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
        mine={removing !== null && removing.id === mine && removing.user === user}
        other={removing !== null && removing.user !== user}
        onClose={() => setRemoving(null)}
        onRemovedMine={() => {
          setThisBrowserID(user, null);
          setResubscribeMarker(user, null);
          stored();
        }}
      />
    </Page>
  );
}

/** The device list's title for the owner filter. */
function listTitle(owner: Owner): string {
  if (owner.kind === "all") return "Everyone's devices";
  if (owner.kind === "user" && owner.name !== "") return `Devices of ${owner.name}`;
  return owner.kind === "own" ? "Your devices" : "Devices of one user";
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
 * panel says so instead; only a stale subscription (dash's push key changed, or a logout) offers
 * the button again, as "Subscribe again", with that device's name. Like the shell's automatic
 * renewal, it replaces the stale row: once the new subscription is registered under another ID
 * (its endpoint changed), the stale one is removed.
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
  // null until the field is edited: the stale row's name, or one made from the user agent.
  const [edited, setName] = useState<string | null>(null);
  const name =
    edited ?? (stale && mineRow ? mineRow.deviceName : defaultDeviceName(navigator.userAgent));
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
      const replaces = stale && mineRow ? mineRow.id : null;
      const sub = await subscribeBrowser(publicKey);
      const info = await registerSubscription(subscriptionRequest(sub, trimmed, notify));
      if (replaces !== null && replaces !== info.id) {
        await deleteSubscription(replaces).catch(() => undefined);
      }
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

/**
 * One device: what it is, its state, its notify settings and what can be done with it. Another
 * user's device (an administrator's view) shows its settings read-only and can only be removed.
 */
function DeviceRow({
  sub,
  mine,
  other,
  showUser,
  onVerify,
  onRemove,
}: {
  sub: PushSubscriptionInfo;
  mine: boolean;
  other: boolean;
  showUser: boolean;
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
        {showUser ? <span className="text-sm text-muted">{sub.user}</span> : null}
        {mine ? <Badge variant="secondary">this browser</Badge> : null}
        <StateBadge state={sub.state} />
        <span className="text-xs text-muted">
          {sub.kind === PushKindUnifiedPush ? "UnifiedPush" : "Web Push"} ·{" "}
          <span className="num">{sub.endpointHost}</span>
        </span>
        <div className="ml-auto flex gap-1">
          {sub.state === PushStateVerified && !other ? (
            <Button size="sm" variant="outline" disabled={testing} onClick={() => void test()}>
              {testing ? <Loader2 className="animate-spin" /> : <Send aria-hidden="true" />}
              Send test push
            </Button>
          ) : null}
          <Button
            size="sm"
            variant="ghost"
            aria-label={
              other ? `Remove ${sub.deviceName} of ${sub.user}` : `Remove ${sub.deviceName}`
            }
            onClick={onRemove}
          >
            <Trash2 aria-hidden="true" />
            Remove
          </Button>
        </div>
      </div>
      {sub.state === PushStateStale ? (
        // Stale: dash's push key changed, the owner's sessions ended (a logout, for a browser), or
        // the grant of the dash app that registered it is gone.
        <p className="text-sm text-warn">
          {other
            ? `dash's push key changed, ${sub.user} logged out (logging out stops push on their browsers), or their dash app was disconnected, so this device receives nothing.`
            : "dash's push key changed, you logged out (logging out stops push on your browsers), or the dash app was disconnected, so this device receives nothing."}{" "}
          {mine
            ? "Subscribe this browser again above."
            : other
              ? `It gets notifications again once ${sub.user} subscribes it again.`
              : "Subscribe again on that device."}
        </p>
      ) : null}
      {sub.state === PushStatePending && !other ? <CodeForm sub={sub} onVerify={onVerify} /> : null}
      {sub.state === PushStateStale && mine ? null : mine ? (
        // Not on a stale row: a change would re-register a subscription dash no longer trusts
        // (made with the old key, its code could never arrive). Subscribing again is the way back.
        <NotifyEditor sub={sub} />
      ) : (
        <NotifySummary notify={sub.notify} owner={other ? sub.user : null} />
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
function NotifySummary({
  notify,
  owner,
}: {
  notify: PushNotify;
  /** Another user's device: only they change it. */
  owner: string | null;
}): JSX.Element {
  return (
    <p className="text-sm text-muted">
      Approvals {notify.approvals ? "on" : "off"} · attention:{" "}
      {ATTENTION.find((a) => a.value === notify.attention)?.label ?? notify.attention}.{" "}
      {owner ? `Only ${owner} changes this, on that device.` : "Change this on the device itself."}
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
          <p className="text-fg">
            The dash Android app is not released yet: it comes in a later release.
          </p>
          <p className="text-muted">
            Scan this code in the dash app to fill in the server address.
          </p>
          <p className="num text-muted">{url}</p>
          <p className="text-muted">It holds the address only, never a credential.</p>
        </div>
      </div>
    </Panel>
  );
}

/**
 * Asks before DELETE …/{id}; for this browser's row it also drops the browser's subscription.
 * `other`: another user's device, which an administrator removes.
 */
function RemoveDialog({
  sub,
  mine,
  other,
  onClose,
  onRemovedMine,
}: {
  sub: PushSubscriptionInfo | null;
  mine: boolean;
  other: boolean;
  onClose: () => void;
  onRemovedMine: () => void;
}): JSX.Element {
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  return (
    <AlertDialog open={!!sub} onOpenChange={(o) => !o && !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            Remove {other ? `${sub?.user}'s ` : ""}
            {sub?.deviceName}?
          </AlertDialogTitle>
          <AlertDialogDescription>
            {other
              ? `It receives no more notifications from dash, until ${sub?.user} subscribes it again.`
              : "It receives no more notifications from dash. Subscribe it again to get them back."}
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
                toast.success(
                  other ? `Removed ${sub.user}'s ${sub.deviceName}` : `Removed ${sub.deviceName}`,
                );
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
