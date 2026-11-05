// The README screenshots (docs/images), taken from demo mode. `make screenshots` builds the SPA
// and the dash binary and runs this script with the binary and the output directory:
//
//   DASH_DEMO_CLOCK=2026-01-15T12:00:00Z node scripts/screenshots.mjs <dash binary> <out dir>
//
// It starts `dash` with DASH_DEMO=true and the fixed demo clock on two free loopback ports (API
// and health), waits for /readyz, captures every page the README shows plus the banner into a temp
// dir, moves them into the output dir only once every capture succeeded (a failed run leaves it
// untouched), and stops dash on every exit path. The PNGs are the same bytes on every run on one
// machine: the demo world and the browser's Date stand still at DASH_DEMO_CLOCK, the viewport,
// timezone, locale and motion are fixed, no request leaves the dash origin, every glyph is drawn
// with a font the SPA bundles (checked on every page), and the PNG text and time chunks are
// dropped.
//
// Needs Playwright's Chromium once: `npx playwright install chromium`.
import { spawn } from "node:child_process";
import {
  copyFileSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { createServer } from "node:net";
import { constants, tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const web = join(fileURLToPath(new URL(".", import.meta.url)), "..");
const [bin, outDir] = process.argv.slice(2);
const clock = process.env.DASH_DEMO_CLOCK;
if (!bin || !outDir || !clock) {
  console.error("usage: DASH_DEMO_CLOCK=<RFC 3339> node scripts/screenshots.mjs <dash> <out dir>");
  process.exit(2);
}
// The PNGs only move to outDir after every capture, so a missing one is found before that work.
if (!statSync(outDir, { throwIfNoEntry: false })?.isDirectory()) {
  console.error(`screenshots: ${outDir} is not a directory`);
  process.exit(2);
}

// The README's images: a 1600x1000 viewport at device scale 1. The pod sheet opens on the pod
// that needs attention most (the most restarts).
const VIEWPORT = { width: 1600, height: 1000 };
const PAGES = [
  ["overview", "/"],
  ["vms", "/vms"],
  ["hardware", "/hardware"],
  ["pods", "/cluster?tab=pods"],
  ["pod-sheet", podSheetPath],
  ["gitops", "/gitops"],
  ["storage", "/storage"],
  ["alerts", "/alerts"],
  ["status", "/status"],
  ["apps", "/apps"],
];
// The banner is 1280x640 (GitHub's social preview size) at device scale 2, with the overview at
// device scale 2 in its window.
const BANNER = { width: 1280, height: 640 };
// How long a page gets after its data is live: the charts' and the tickers' first frames.
const SETTLE_MS = 2500;

/**
 * n distinct free loopback ports: every listener on port 0 stays open until all n are bound, so
 * the kernel cannot hand the same port out twice, and only then are they all closed.
 */
async function freePorts(n) {
  const servers = [];
  try {
    for (let i = 0; i < n; i++) {
      const srv = createServer();
      srv.unref();
      servers.push(srv);
      await new Promise((resolve, reject) => {
        srv.once("error", reject);
        srv.listen(0, "127.0.0.1", resolve);
      });
    }
    return servers.map((srv) => srv.address().port);
  } finally {
    await Promise.all(
      servers
        .filter((srv) => srv.listening)
        .map((srv) => new Promise((resolve) => srv.close(resolve))),
    );
  }
}

/**
 * Moves every file of from into to: a rename, or a copy and an unlink when from and to are on
 * different filesystems (EXDEV), as tmpdir often is.
 */
function moveAll(from, to) {
  for (const name of readdirSync(from)) {
    const src = join(from, name);
    const dst = join(to, name);
    try {
      renameSync(src, dst);
    } catch (err) {
      if (err.code !== "EXDEV") {
        throw err;
      }
      copyFileSync(src, dst);
      unlinkSync(src);
    }
  }
}

/** Starts dash in demo mode and resolves once /readyz answers 200; rejects if it exits. */
async function startDash(apiPort, healthPort) {
  const env = {
    PATH: process.env.PATH ?? "",
    HOME: process.env.HOME ?? "",
    DASH_DEMO: "true",
    DASH_DEMO_CLOCK: clock,
    DASH_SERVER_LISTEN: `127.0.0.1:${apiPort}`,
    DASH_SERVER_HEALTH_LISTEN: `127.0.0.1:${healthPort}`,
    DASH_SERVER_PUBLIC_URL: `http://127.0.0.1:${apiPort}`,
  };
  const child = spawn(bin, [], { env, stdio: ["ignore", "pipe", "pipe"] });
  let log = "";
  child.stdout.on("data", (b) => {
    log += b;
  });
  child.stderr.on("data", (b) => {
    log += b;
  });
  const exited = new Promise((resolve) => child.once("exit", (code, sig) => resolve(sig ?? code)));
  const deadline = Date.now() + 30_000;
  for (;;) {
    const status = await Promise.race([
      fetch(`http://127.0.0.1:${healthPort}/readyz`).then(
        (r) => r.status,
        () => 0,
      ),
      exited.then((how) => `exit ${how}`),
    ]);
    if (status === 200) {
      return { child, exited, log: () => log };
    }
    if (typeof status === "string" || Date.now() > deadline) {
      child.kill("SIGKILL");
      throw new Error(`dash did not become ready (${status}):\n${log}`);
    }
    await new Promise((r) => setTimeout(r, 100));
  }
}

/** Stops dash: SIGTERM, then SIGKILL after 10 s. */
async function stopDash(dash) {
  if (dash.child.exitCode !== null || dash.child.signalCode !== null) {
    return;
  }
  dash.child.kill("SIGTERM");
  const timer = setTimeout(() => dash.child.kill("SIGKILL"), 10_000);
  await dash.exited;
  clearTimeout(timer);
}

/** The pod sheet of the pod with the most restarts. */
async function podSheetPath(origin) {
  const r = await fetch(`${origin}/api/v1/topics/kubernetes.cluster`);
  if (!r.ok) {
    throw new Error(`kubernetes.cluster: ${r.status}`);
  }
  const { data } = await r.json();
  const pod = [...data.pods].sort((a, b) => b.restarts - a.restarts)[0];
  return `/cluster/pods/${encodeURIComponent(pod.namespace)}/${encodeURIComponent(pod.name)}`;
}

/** Drops the PNG chunks that carry text or a time (tEXt, zTXt, iTXt, tIME). */
function stripPNG(buf) {
  const drop = new Set(["tEXt", "zTXt", "iTXt", "tIME"]);
  const parts = [buf.subarray(0, 8)];
  for (let off = 8; off < buf.length; ) {
    const len = buf.readUInt32BE(off);
    const end = off + 12 + len;
    if (!drop.has(buf.toString("latin1", off + 4, off + 8))) {
      parts.push(buf.subarray(off, end));
    }
    off = end;
  }
  return Buffer.concat(parts);
}

/** A browser context fixed for repeatable pixels, allowed to reach origin only. */
async function newContext(browser, origin, viewport, deviceScaleFactor, local = {}) {
  const ctx = await browser.newContext({
    viewport,
    deviceScaleFactor,
    reducedMotion: "reduce",
    colorScheme: "dark",
    timezoneId: "UTC",
    locale: "en-US",
    serviceWorkers: "block",
  });
  await ctx.clock.setFixedTime(new Date(clock));
  await ctx.route("**/*", (route) => {
    const url = new URL(route.request().url());
    const body = url.origin === origin ? local[url.pathname] : undefined;
    if (body) {
      return route.fulfill(body);
    }
    if (url.origin === origin || url.protocol === "data:" || url.protocol === "blob:") {
      return route.continue();
    }
    return route.abort("blockedbyclient");
  });
  return ctx;
}

// Every glyph must come from a web font: the SPA loads only the fonts it bundles (no request
// leaves the dash origin), and CSS.getPlatformFontsForNode reports those as custom fonts. A page
// whose text Chromium drew with a font of the machine (a fallback for a glyph the bundled subsets
// lack) fails the run rather than ship machine-dependent pixels.
async function assertBundledFonts(page, what) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("DOM.enable");
  await cdp.send("CSS.enable");
  const { root } = await cdp.send("DOM.getDocument", { depth: -1 });
  const system = new Map();
  const walk = async (node) => {
    if (node.nodeType === 1 && node.children?.some((c) => c.nodeType === 3 && c.nodeValue.trim())) {
      const { fonts } = await cdp.send("CSS.getPlatformFontsForNode", { nodeId: node.nodeId });
      for (const f of fonts.filter((f) => !f.isCustomFont)) {
        system.set(f.familyName, (system.get(f.familyName) ?? 0) + f.glyphCount);
      }
    }
    for (const c of node.children ?? []) {
      await walk(c);
    }
  };
  await walk(root);
  await cdp.detach();
  if (system.size > 0) {
    throw new Error(`${what}: glyphs drawn with system fonts: ${JSON.stringify([...system])}`);
  }
}

/** Opens path, waits for the live stream, the fonts and the first frames, and screenshots it. */
async function capture(ctx, origin, path) {
  const page = await ctx.newPage();
  await page.goto(origin + path);
  await page.getByText("LIVE", { exact: true }).waitFor();
  await page.evaluate(() => document.fonts.ready);
  await page.waitForTimeout(SETTLE_MS);
  await assertBundledFonts(page, path);
  const png = await page.screenshot({ animations: "disabled", caret: "hide" });
  await page.close();
  return png;
}

async function bannerPNG(browser, origin) {
  const shotCtx = await newContext(browser, origin, VIEWPORT, 2);
  const overview = await capture(shotCtx, origin, "/");
  await shotCtx.close();

  const file = (p, contentType) => ({ body: readFileSync(join(web, p)), contentType });
  const ctx = await newContext(browser, origin, BANNER, 2, {
    "/__banner/": file("scripts/screenshots/banner.html", "text/html"),
    "/__banner/overview.png": { body: overview, contentType: "image/png" },
    "/__banner/inter.woff2": file(
      "node_modules/@fontsource-variable/inter/files/inter-latin-wght-normal.woff2",
      "font/woff2",
    ),
    "/__banner/jetbrains-mono.woff2": file(
      "node_modules/@fontsource-variable/jetbrains-mono/files/jetbrains-mono-latin-wght-normal.woff2",
      "font/woff2",
    ),
  });
  const page = await ctx.newPage();
  await page.goto(`${origin}/__banner/`);
  await page.evaluate(() => document.fonts.ready);
  await page.locator("img").evaluate((img) => img.decode());
  await assertBundledFonts(page, "banner");
  const png = await page.screenshot({ animations: "disabled", caret: "hide" });
  await ctx.close();
  return png;
}

async function main() {
  const [apiPort, healthPort] = await freePorts(2);
  const origin = `http://127.0.0.1:${apiPort}`;
  // The PNGs land in a temp dir first and move to outDir only once every capture succeeded, so a
  // failed or interrupted run leaves outDir as it was.
  const tmp = mkdtempSync(join(tmpdir(), "dash-screenshots-"));
  let dash;
  let browser;
  const stop = async () => {
    try {
      await browser?.close();
      if (dash) {
        await stopDash(dash);
      }
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  };
  for (const sig of ["SIGINT", "SIGTERM", "SIGHUP"]) {
    process.once(sig, () => {
      stop().finally(() => process.exit(128 + constants.signals[sig]));
    });
  }
  try {
    dash = await startDash(apiPort, healthPort);
    browser = await chromium.launch({
      args: ["--force-color-profile=srgb", "--font-render-hinting=none", "--disable-lcd-text"],
    });
    const ctx = await newContext(browser, origin, VIEWPORT, 1);
    for (const [name, path] of PAGES) {
      const at = typeof path === "function" ? await path(origin) : path;
      writeFileSync(join(tmp, `${name}.png`), stripPNG(await capture(ctx, origin, at)));
      console.log(`screenshots: ${name}.png (${at})`);
    }
    await ctx.close();
    writeFileSync(join(tmp, "banner.png"), stripPNG(await bannerPNG(browser, origin)));
    console.log("screenshots: banner.png");
    moveAll(tmp, outDir);
  } finally {
    await stop();
  }
}

main().catch((err) => {
  console.error(`screenshots: ${err.stack ?? err}`);
  process.exit(1);
});
