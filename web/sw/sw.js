// dash's service worker (P7.6), hand-written and small so it stays auditable (no Workbox, ADR
// 0002 S.11). vite-plugins/service-worker.ts emits it as dist/sw.js with the two placeholders
// below filled in: the build hash, so every deploy changes the worker's bytes (the browser's
// update check compares them), and the precache list, which holds only the hashed /assets/*,
// /offline.html and the icons.
//
// - Precached files are answered from the cache, falling back to the network.
// - Navigations go to the network first; when it fails, the cached /offline.html answers (with
//   the headers it was fetched with, so its own CSP applies). index.html is never cached: it
//   carries a per-response CSP nonce. A navigation the worker forwards reaches the server with
//   Sec-Fetch-Dest: empty (dash does not read it).
// - /api/, /oauth/, /mcp, /auth/, /login, /setup and /.well-known/ (and below each), every other
//   origin and every non-GET request are never answered: the browser fetches them as if no worker
//   existed, so the event stream, the cookies and the sign-in are untouched and nothing of them is
//   cached. No navigation preload: with it, the browser would send such a navigation twice (once
//   as the preload, once when the worker leaves it), and an OIDC callback's code would be replayed.
// - Nothing is ever written to the cache but the precache list, at install, all or nothing.
// - On activate every cache but this build's is deleted.

const BUILD = "__DASH_SW_BUILD__";
const PRECACHE = /* __DASH_SW_PRECACHE__ */ [];

const CACHE = `dash-${BUILD}`;
const OFFLINE = "/offline.html";
const PASS_THROUGH = ["/api", "/oauth", "/mcp", "/auth", "/login", "/setup", "/.well-known"];
const precached = new Set(PRECACHE);

/** True when the worker leaves a same-origin path to the browser. */
function passThrough(pathname) {
  return PASS_THROUGH.some((root) => pathname === root || pathname.startsWith(`${root}/`));
}

self.addEventListener("install", (event) => {
  event.waitUntil(
    caches
      .open(CACHE)
      .then((cache) => cache.addAll(PRECACHE))
      .then(() => self.skipWaiting()),
  );
});

self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  );
});

self.addEventListener("fetch", (event) => {
  const request = event.request;
  if (request.method !== "GET") {
    return;
  }
  const url = new URL(request.url);
  if (url.origin !== self.location.origin || passThrough(url.pathname)) {
    return;
  }
  if (request.mode === "navigate") {
    event.respondWith(
      fetch(request).catch(() =>
        caches
          .open(CACHE)
          .then((cache) => cache.match(OFFLINE))
          .then((page) => page ?? Response.error()),
      ),
    );
    return;
  }
  if (precached.has(url.pathname)) {
    event.respondWith(
      caches
        .open(CACHE)
        .then((cache) => cache.match(request, { ignoreSearch: true }))
        .then((hit) => hit ?? fetch(request)),
    );
  }
});

// Web Push (P7.7). Every push shows a notification (userVisibleOnly), all inside waitUntil. The
// payload is dash's PushMessage JSON {type, id, title, body, url, ts}; a verify push carries
// {type: "verify", code} and is also posted to every window of the origin, so an open Settings →
// Devices page submits the code itself. A payload that is not such JSON still shows "dash". The
// icon and the badge are dash's own (the worker's policy allows img-src 'self' for them).
const VERIFY_MESSAGE = "dash-push-verify";
const ICON = "/icons/icon-192.png";

/** The push's JSON payload, or null when it has none or it does not parse. */
function pushMessage(event) {
  try {
    const msg = event.data?.json();
    return msg !== null && typeof msg === "object" ? msg : null;
  } catch {
    return null;
  }
}

/** Shows the verify code and posts it to every window of the origin. */
function verify(code, url) {
  return Promise.all([
    self.registration.showNotification("dash: verify this device", {
      body: `Code ${code}`,
      icon: ICON,
      badge: ICON,
      tag: "dash-push-verify",
      data: { url },
    }),
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((wins) => {
      for (const w of wins) {
        w.postMessage({ type: VERIFY_MESSAGE, code });
      }
    }),
  ]);
}

self.addEventListener("push", (event) => {
  const msg = pushMessage(event);
  const url = typeof msg?.url === "string" ? msg.url : "/";
  if (msg?.type === "verify" && typeof msg.code === "string") {
    event.waitUntil(verify(msg.code, url));
    return;
  }
  const options = {
    body: typeof msg?.body === "string" ? msg.body : "",
    icon: ICON,
    badge: ICON,
    data: { url },
  };
  if (typeof msg?.id === "string" && msg.id !== "") {
    options.tag = msg.id;
  }
  const title = typeof msg?.title === "string" && msg.title !== "" ? msg.title : "dash";
  event.waitUntil(self.registration.showNotification(title, options));
});

/** The URL a tap opens: the notification's url when it is on the worker's own origin, else /. */
function clickTarget(url) {
  const home = new URL("/", self.location.origin).href;
  if (typeof url !== "string") {
    return home;
  }
  try {
    const target = new URL(url, self.location.origin);
    return target.origin === self.location.origin ? target.href : home;
  } catch {
    return home;
  }
}

// A tap focuses a window already on the target, or opens one.
self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const target = clickTarget(event.notification.data?.url);
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((wins) => {
      const open = wins.find((w) => w.url === target);
      return open ? open.focus() : self.clients.openWindow(target);
    }),
  );
});
