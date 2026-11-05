// The PWA browser smoke (P7.6). `make pwa-smoke` builds the SPA and dash and runs this script with
// the binary:
//
//   node scripts/pwa-smoke.mjs <dash binary>
//
// It starts dash in demo mode on free loopback ports, behind a loopback proxy that counts the
// requests reaching dash (http://127.0.0.1 is a secure context, so the service worker registers),
// and drives Playwright's pinned Chromium through the P7.6 acceptance:
//
// 1. installable: CDP Page.getInstallabilityErrors reports nothing;
// 2. a sign-in callback navigation (/auth/callback?code=…) reaches dash exactly once, sent by the
//    browser as a document, never by the worker; a page navigation the worker forwards, once too;
// 3. after a browsing session (the overview with its live stream, a plugin page and the alerts),
//    Cache Storage holds exactly the worker's precache list and no /api, /oauth or /mcp URL. The
//    whole list cached also proves the worker's own CSP let its fetches through;
// 4. reloading offline shows the offline page, answered by the worker, with its CSS applied;
// 5. no CSP violation: in any document (securitypolicyviolation events and the console) or in the
//    worker (its securitypolicyviolation events and its console).
//
// It prints one line per check and exits 1 listing every check that failed, or after 120 s. dash,
// the proxy and Chromium are stopped on every exit path; a signal exits with 128 plus its number.
// Needs Playwright's pinned Chromium once:
//   cd web && npm ci --ignore-scripts && npx playwright install chromium

import { mkdtempSync, rmSync } from "node:fs";
import { createServer, request as httpRequest } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { chromium } from "playwright";
import { exit, freePorts, onSignals, startDash, stopAll } from "./lib/demo.mjs";

const [bin] = process.argv.slice(2);
if (!bin) {
  console.error("usage: node scripts/pwa-smoke.mjs <dash binary>");
  process.exit(2);
}

// The paths the worker must never answer, so none may ever be in Cache Storage.
const NEVER_CACHED = /^\/(?:api|oauth|mcp)(?:\/|$)/;
// The console text Chromium logs for a CSP violation, in a document or a worker.
const CSP_CONSOLE =
  /Content Security Policy|Refused to (?:load|connect|apply|execute|evaluate|frame|create)/i;

const failures = [];
function check(ok, what, detail = "") {
  console.log(`pwa-smoke: ${ok ? "ok  " : "FAIL"} ${what}${detail ? `: ${detail}` : ""}`);
  if (!ok) {
    failures.push(what);
  }
}

/** The precache list dash's /sw.js carries. */
async function precacheOf(origin) {
  const r = await fetch(`${origin}/sw.js`);
  const list = /^const PRECACHE = (\[.*\]);$/m.exec(await r.text())?.[1];
  if (!r.ok || !list) {
    throw new Error(`/sw.js: ${r.status}, precache list ${list ? "found" : "missing"}`);
  }
  return JSON.parse(list);
}

/** Opens path in page and waits until the live stream is connected. */
async function visit(page, origin, path) {
  await page.goto(origin + path);
  await page.getByText("LIVE", { exact: true }).waitFor({ timeout: 15_000 });
}

/**
 * A loopback HTTP proxy to dash on port target that records every request it forwards (the path
 * and query, and Sec-Fetch-Dest), so the smoke can count what reached dash.
 */
function countingProxy(target) {
  const seen = [];
  const server = createServer((req, res) => {
    seen.push({ url: req.url, dest: req.headers["sec-fetch-dest"] ?? "" });
    const upstream = httpRequest(
      { host: "127.0.0.1", port: target, method: req.method, path: req.url, headers: req.headers },
      (up) => {
        res.writeHead(up.statusCode ?? 502, up.rawHeaders);
        up.pipe(res);
      },
    );
    upstream.on("error", () => res.destroy());
    res.on("close", () => upstream.destroy());
    req.pipe(upstream);
  });
  return { server, seen };
}

async function smoke(ctx, origin, seen) {
  const violations = [];
  // Every document reports its violations, the offline page too: init scripts and bindings reach
  // a page whatever its CSP.
  await ctx.exposeBinding("__dashSmokeViolation", (_src, v) => violations.push(v));
  await ctx.addInitScript(() => {
    document.addEventListener("securitypolicyviolation", (e) => {
      window.__dashSmokeViolation(
        `${location.pathname}: ${e.effectiveDirective} blocked ${e.blockedURI || "an inline"}`,
      );
    });
  });
  const onConsole = (where) => (m) => {
    if (CSP_CONSOLE.test(m.text())) {
      violations.push(`${where} console: ${m.text()}`);
    }
  };
  ctx.on("console", onConsole("page"));
  ctx.on("serviceworker", (w) => w.on("console", onConsole("worker")));

  const page = await ctx.newPage();
  await visit(page, origin, "/");
  const controlled = await page
    .waitForFunction(() => navigator.serviceWorker.controller !== null, null, { timeout: 15_000 })
    .then(
      () => true,
      () => false,
    );
  const [worker] = ctx.serviceWorkers();
  check(
    controlled && worker?.url() === `${origin}/sw.js`,
    "the worker installs and controls the page",
    controlled ? worker?.url() : "no controller after 15 s: its install (the precache) failed?",
  );
  if (!controlled) {
    return;
  }
  // The worker's own violations from here on (its install ran before anyone could listen; the
  // cache check below covers it).
  await worker?.evaluate(() => {
    self.__dashSmokeViolations = [];
    self.addEventListener("securitypolicyviolation", (e) => {
      self.__dashSmokeViolations.push(`worker: ${e.effectiveDirective} blocked ${e.blockedURI}`);
    });
  });

  // 1. Installable.
  const cdp = await ctx.newCDPSession(page);
  const { installabilityErrors } = await cdp.send("Page.getInstallabilityErrors");
  check(
    installabilityErrors.length === 0,
    "installable (Page.getInstallabilityErrors is empty)",
    JSON.stringify(installabilityErrors),
  );
  const { url: manifestURL, errors: manifestErrors } = await cdp.send("Page.getAppManifest");
  check(
    manifestURL === `${origin}/manifest.webmanifest` && manifestErrors.length === 0,
    "the manifest parses without errors",
    `${manifestURL} ${JSON.stringify(manifestErrors)}`,
  );

  // 2. A sign-in callback is the browser's: one request, sent as a document, not by the worker
  //    (its code is good for one exchange).
  const callback = "/auth/callback?code=smoke-code&state=smoke-state";
  let from = seen.length;
  const res = await page.goto(origin + callback);
  const hits = seen.slice(from).filter((r) => r.url === callback);
  check(
    hits.length === 1 && hits[0].dest === "document" && res?.fromServiceWorker() === false,
    "a sign-in callback navigation reaches dash once, from the browser",
    `${hits.map((h) => `dest=${h.dest}`).join(", ")}; from the worker: ${res?.fromServiceWorker()}`,
  );

  // 3. A browsing session, then Cache Storage. A page navigation the worker forwards reaches dash
  //    once too.
  await visit(page, origin, "/cluster");
  from = seen.length;
  await visit(page, origin, "/alerts");
  const alerts = seen.slice(from).filter((r) => r.url === "/alerts");
  check(
    alerts.length === 1,
    "a navigation the worker forwards reaches dash once",
    alerts.map((h) => `dest=${h.dest}`).join(", "),
  );
  await visit(page, origin, "/");
  const cached = await page.evaluate(async () => {
    const urls = [];
    for (const name of await caches.keys()) {
      const cache = await caches.open(name);
      for (const r of await cache.keys()) {
        urls.push(new URL(r.url).pathname);
      }
    }
    return urls;
  });
  const forbidden = cached.filter((p) => NEVER_CACHED.test(p));
  check(
    forbidden.length === 0,
    "Cache Storage holds no /api, /oauth or /mcp URL",
    forbidden.join(", "),
  );
  const precache = await precacheOf(origin);
  const same =
    cached.length === precache.length && [...cached].sort().join() === [...precache].sort().join();
  check(
    same,
    "Cache Storage holds exactly the precache list",
    `${cached.length} cached, ${precache.length} listed`,
  );

  // 4. Offline.
  await ctx.setOffline(true);
  const offline = await page.reload();
  const csp = (await offline?.allHeaders())?.["content-security-policy"] ?? "";
  check(
    offline?.fromServiceWorker() === true &&
      (await page.title()) === "dash: offline" &&
      (await page.locator("h1").textContent()) === "dash is offline",
    "reloading offline shows the offline page from the worker",
    `${offline?.status()} ${await page.title()}`,
  );
  const background = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  check(
    background === "rgb(4, 6, 11)" && /style-src 'sha256-/.test(csp),
    "the offline page's CSS applies under its own policy",
    `background ${background}; ${csp}`,
  );
  await ctx.setOffline(false);
  await visit(page, origin, "/");
  check((await page.title()) === "dash", "back online, the app loads again");

  // 5. CSP violations.
  const workerViolations =
    (await ctx.serviceWorkers()[0]?.evaluate(() => self.__dashSmokeViolations)) ?? [];
  const all = [...violations, ...workerViolations];
  check(all.length === 0, "no CSP violations", all.join("\n  "));
}

async function main() {
  const [apiPort, healthPort, proxyPort] = await freePorts(3);
  const origin = `http://127.0.0.1:${proxyPort}`;
  // A profile on disk: Chromium reports a context without one (incognito) as not installable.
  const profile = mkdtempSync(join(tmpdir(), "dash-pwa-smoke-"));
  const proxy = countingProxy(apiPort);
  let launching;
  const stop = async () => {
    try {
      try {
        // A launch still in progress is awaited, so Chromium writes nothing after the rm below.
        await launching?.then(
          (ctx) => ctx.close(),
          () => {},
        );
      } finally {
        proxy.server.closeAllConnections();
        proxy.server.close(() => {});
        await stopAll();
      }
    } finally {
      rmSync(profile, { recursive: true, force: true });
    }
  };
  onSignals(stop);
  // Nothing below has a timeout of its own (a CDP call, an evaluate in the worker): the whole run
  // gets two minutes.
  setTimeout(() => {
    console.error("pwa-smoke: no result after 120 s");
    stop().finally(() => exit(1));
  }, 120_000).unref();
  try {
    await new Promise((resolve, reject) => {
      proxy.server.once("error", reject);
      proxy.server.listen(proxyPort, "127.0.0.1", resolve);
    });
    await startDash(bin, apiPort, healthPort, { DASH_SERVER_PUBLIC_URL: origin });
    // channel "chromium": Playwright's full Chromium in the new headless mode. The default
    // headless shell has no install support and reports no installability error at all.
    launching = chromium.launchPersistentContext(profile, {
      channel: "chromium",
      serviceWorkers: "allow",
    });
    const ctx = await launching;
    console.log(`pwa-smoke: dash demo at ${origin}, chromium ${ctx.browser()?.version() ?? ""}`);
    await smoke(ctx, origin, proxy.seen);
  } finally {
    await stop();
  }
  if (failures.length > 0) {
    console.error(`pwa-smoke: ${failures.length} check(s) failed`);
    exit(1);
  }
  console.log("pwa-smoke: every check passed");
}

main().catch((err) => {
  console.error(`pwa-smoke: ${err.stack ?? err}`);
  exit(1);
});
