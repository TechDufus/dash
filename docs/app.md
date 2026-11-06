# Installing dash as an app

dash is a progressive web app: a browser can install it, and it then opens in its own window
with its own icon, without the address bar. There is nothing to turn on. Behind an identity proxy
such as the [oauth2-proxy sidecar](deploy/kubernetes.md) the browser fetches the app's manifest
with the proxy's session cookie, so install from a signed-in tab.

A native Android app is planned in a separate repository and is not released yet; what it needs
from dash is in [the Android app contract](clients/android.md).

## Requirements

- **HTTPS.** Browsers install, and run the service worker, only on a secure origin: `https://`, or
  `http://localhost` and `http://127.0.0.1` on the machine itself. A dash served over plain HTTP on
  another address works as a web page but cannot be installed.
- **`server.csp` may stay at its default** (`enforce`). The worker and the offline page carry
  policies of their own, enforced whatever `server.csp` says: they allow dash's own origin only
  and cannot blank the UI ([security model](security.md)).

## Install

- **Chrome, Edge and other Chromium browsers (desktop):** the install icon at the right of the
  address bar, or the menu → *Cast, save and share* → *Install page as app* (the wording varies by
  version).
- **Chrome on Android:** the menu → *Add to Home screen* → *Install*.
- **Safari on iPhone and iPad:** the Share button → *Add to Home Screen*. iOS and iPadOS deliver
  [push notifications](push.md) only to an app added to the home screen, on 16.4 or later.
- **Safari on macOS (14 or later):** *File* → *Add to Dock*.

The installed app is the same dash, and its data is live from the server as in a tab. On iOS and
in Safari on macOS the app keeps its own sign-in: signing out in Safari does not sign it out, and
it may ask you to sign in again.

## What the service worker keeps

The worker (`/sw.js`) keeps the files of the web UI that never change under one name (the hashed
scripts, styles and fonts), the icons and an offline page. Pages always come from the server
first; when the server cannot be reached, the offline page says so, and *Try again* reloads the
page you were on. Nothing from `/api`, `/oauth`, `/mcp`, the sign-in routes or another site is
ever answered or kept by the worker, so no dashboard data is stored on the device; the sign-in
routes go to the server exactly as without a worker. A page navigation the worker forwards reaches
the server with `Sec-Fetch-Dest: empty` instead of `document`; dash does not read that header.
Each release that changes the web UI installs a new worker, which deletes the files of the old
one.

## Rolling back

A release before the app support answers `/sw.js` with the web page, so a browser cannot update
the worker it already has. The old worker stays registered and keeps serving its precached files
until it updates; pages still come from the older server, so dash works, but the worker and its
files stay on the device. To remove them, open the browser's developer tools → *Application* →
*Service workers* → *Unregister*, then *Storage* → *Clear site data*, or clear the site's data in
the browser's settings. Clearing the site's data also deletes its cookies, so it signs you out.
