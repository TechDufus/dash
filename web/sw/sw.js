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

// Web Push arrives with P7.7; until then no subscription exists and these never run. A push must
// still show a notification (userVisibleOnly), and a tap opens dash.
self.addEventListener("push", (event) => {
  event.waitUntil(self.registration.showNotification("dash", { icon: "/icons/icon-192.png" }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil(self.clients.openWindow("/"));
});
