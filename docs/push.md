# Push notifications

dash can notify a phone or a browser when something in the lab needs attention, even with dash
closed: Web Push to browsers and installed apps, and UnifiedPush (for example through ntfy) to a
native client. Each person registers their own devices in **Settings → Devices** and chooses what
reaches each one.

## What push needs

Push is off unless all of these hold; at startup one INFO line, `push: off`, names what is missing:

- `auth.mode` is `local`, `oidc` or `proxy`. `auth.mode=none` has no push, and dev and demo mode
  never send one.
- A writable data dir (`DASH_DATA_DIR`): the VAPID key pair and the subscriptions live in
  `credentials.yaml`.
- `DASH_SECRET_KEY`: the VAPID private key and each device's endpoint and keys are sealed with it.
- A contact for the VAPID signature: `push.contact`, or `server.publicURL` when it is https.

The settings are `push.contact` and `push.allowPrivate` ([configuration](configuration.md)); both
need a restart.

## Browsers and iOS

Any browser with Web Push works: Chrome, Edge and Firefox on the desktop and Android, Safari on
macOS. On iPhone and iPad push needs:

- iOS or iPadOS 16.4 or later;
- dash added to the Home Screen (Safari, Share → Add to Home Screen; see [the app](app.md)), and
  opened from its icon;
- the permission asked from a button: dash asks only when you press **Subscribe**, never on load.

## Settings → Devices

- **Subscribe** asks for the notification permission and registers this browser, with approvals on
  and attention at **Critical**. A user holds at most 10 devices.
- **Verify:** dash pushes a code to the new device and nothing else until the code comes back. The
  page submits it by itself when the notification arrives; otherwise type the code from the
  notification. Five wrong codes delete the device, and so does staying unverified for 10 minutes.
- **What reaches the device** (attention: Critical, Warnings or Off; approvals on or off) is
  changed on the device itself; the other devices show their settings read-only.
- **Send test push** sends one to a verified device (one a minute per device).
- **The QR code** holds dash's address for the Android app to scan, never a credential.

A device marked **stale** receives nothing: subscribe again on it.

## Removed and signed-out users

In the `local` and `oidc` modes a device follows its owner's sessions: a logout, **Log out
everywhere** and **Sign out all users** turn the owner's devices (all devices, for the last one)
stale, and they get nothing until they subscribe again. When an attention push is sent, dash also
checks that the owner may still sign in: in `local` mode only the administrator account, in `oidc`
mode a user still in `auth.oidc.allowedUsers`.

Two cases are beyond what dash can see. In `proxy` mode the identity proxy decides who gets in,
and with `auth.oidc.allowedGroups` dash cannot tell without a login that a user left the group.
When you remove such a user, remove their devices too: an administrator picks the user (or all
users) on Settings → Devices and removes the devices there (`GET /api/v1/push/subscriptions?user=`
and `DELETE /api/v1/push/subscriptions/{id}`).

## Attention pushes

An attention item still on the overview 2 minutes after dash first saw it is pushed once to each
verified device whose threshold it meets (Critical takes critical items, Warnings both). It is
pushed again only after it cleared and came back. The user's role is checked again when the push
is sent. A user receives at most 30 attention pushes an hour; the rest become one summary at the
end of the hour, "N more items need attention". A push opens the item's page.

## UnifiedPush and ntfy

A native client registers its UnifiedPush endpoint, for example on an ntfy server, with the same
verification. dash posts to global addresses only and always over https. For an ntfy on the LAN,
list it in `push.allowPrivate` (a host name such as `ntfy.example.com`, an address or a CIDR);
link-local addresses never pass.

## Limits and retries

- A payload is at most 3 KiB before encryption.
- A device receives at most 60 pushes an hour.
- A user registers at most 10 devices a minute, registering the same device again included (429).
- A 429, a 5xx or a failed connection is tried 3 more times, after 1, 4 and 16 seconds, or after the
  `Retry-After` the push service asks for, up to 60 seconds. A push waiting to retry holds no
  sending slot, so one slow push service delays no one else.
- dash sends at most 16 pushes at once, and at most 4 to one push service. Up to 1024 more wait
  their turn; past that the oldest attention, test or verify push is dropped and counted as
  `dash_push_sent_total{result="dropped"}`. An approval is never dropped.
- A 404 or 410 means the push service dropped the subscription: dash deletes it.

## Rotating DASH_SECRET_KEY

- **With `DASH_SECRET_KEY_PREVIOUS` set** to the old key, dash re-seals the push data with the new
  key at startup, and every device keeps working.
- **Without it**, the stored VAPID key no longer opens: dash makes a new one, every Web Push device
  turns stale, and a WARN at startup says how many. Each device must subscribe again. A UnifiedPush
  device keeps working when its own sealed fields still open.

## Privacy

- Payloads carry lab text only (an item's title, source and detail, and a link into dash), never a
  secret or a credential.
- Every payload is end-to-end encrypted (RFC 8291): the push service sees only ciphertext.
- An endpoint is a capability URL, so dash never logs, audits or stores it in clear; logs name the
  subscription ID and the endpoint's host.
