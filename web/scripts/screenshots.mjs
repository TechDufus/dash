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
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";
import { exit, freePorts, onSignals, startDash, stopAll } from "./lib/demo.mjs";

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
  ["hosts", "/hosts"],
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
  let browser;
  const stop = async () => {
    try {
      try {
        await browser?.close();
      } finally {
        await stopAll();
      }
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  };
  onSignals(stop);
  try {
    await startDash(bin, apiPort, healthPort, { DASH_DEMO_CLOCK: clock });
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
  exit(1);
});
