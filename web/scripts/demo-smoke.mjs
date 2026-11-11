// The static demo's smoke test (docs/development.md, "Static demo"). `make demo` and
// `make demo-smoke` run it on the built demo with its recording in it:
//
//   node scripts/demo-smoke.mjs <dist-demo dir>
//
// It serves the directory under a sub-path (/demo/, never the root, as GitHub Pages serves it
// under the site) from a tiny static server on a free loopback port and crawls it with
// Playwright's headless Chromium along the recorder's plan (lib/demo-crawl.mjs). It fails on any
// GET the recording has no answer for, any console error or uncaught exception, any failed
// request or error status, any request that leaves the origin, a page with an empty main area or
// without the LIVE indicator, a "Copy context for AI" control that does not end in its success
// toast, and unless a write (a GitOps reconcile) ends in the read-only message. A run ends after
// 10 minutes at most.
//
// Needs Playwright's Chromium once: `npx playwright install chromium`.
import { existsSync, readFileSync, statSync } from "node:fs";
import { createServer } from "node:http";
import { extname, join, normalize, resolve, sep } from "node:path";
import { chromium } from "playwright";
import { exit, freePorts, onSignals } from "./lib/demo.mjs";
import { appRoute, crawl, derivedRoutes, trackRequests, unfinished } from "./lib/demo-crawl.mjs";

const [dirArg] = process.argv.slice(2);
if (!dirArg) {
  console.error("usage: node scripts/demo-smoke.mjs <dist-demo dir>");
  process.exit(2);
}
const dir = resolve(dirArg);
for (const f of ["index.html", "recording/manifest.json"]) {
  if (!existsSync(join(dir, f))) {
    console.error(`demo-smoke: ${join(dir, f)} is missing (build the demo and record it first)`);
    process.exit(2);
  }
}

const PREFIX = "/demo/";
const TIMEOUT_MS = 10 * 60_000;
/** A piece of the message every write answers (src/demo/player.ts). */
const READ_ONLY_TEXT = "This is a recorded, read-only demo of dash";
const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json",
  ".txt": "text/plain; charset=utf-8",
  ".woff2": "font/woff2",
  ".woff": "font/woff",
  ".svg": "image/svg+xml",
  ".png": "image/png",
};

/** Serves dir under PREFIX, as a static host does: files only, index.html for a directory. */
function serve(port) {
  const server = createServer((req, res) => {
    const path = decodeURIComponent(new URL(req.url, "http://x").pathname);
    if (req.method !== "GET" || !path.startsWith(PREFIX)) {
      res.writeHead(404).end();
      return;
    }
    let file = normalize(join(dir, path.slice(PREFIX.length)));
    if (file !== dir && !file.startsWith(dir + sep)) {
      res.writeHead(404).end();
      return;
    }
    if (statSync(file, { throwIfNoEntry: false })?.isDirectory()) {
      file = join(file, "index.html");
    }
    if (!statSync(file, { throwIfNoEntry: false })?.isFile()) {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(200, { "Content-Type": TYPES[extname(file)] ?? "application/octet-stream" });
    res.end(readFileSync(file));
  });
  return new Promise((ok, fail) => {
    server.once("error", fail);
    server.listen(port, "127.0.0.1", () => ok(server));
  });
}

async function main() {
  const [port] = await freePorts(1);
  const origin = `http://127.0.0.1:${port}`;
  const base = origin + PREFIX;
  const server = await serve(port);
  let browser;
  const stop = async () => {
    await browser?.close();
    await new Promise((ok) => server.close(ok));
  };
  onSignals(stop);
  const timer = setTimeout(() => {
    console.error(`demo-smoke: no result after ${TIMEOUT_MS / 1000} s`);
    stop().finally(() => exit(1));
  }, TIMEOUT_MS);
  try {
    browser = await chromium.launch();
    const ctx = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      reducedMotion: "reduce",
      colorScheme: "dark",
      timezoneId: "UTC",
      locale: "en-US",
      serviceWorkers: "block",
    });
    // "Copy context for AI" writes to the clipboard (lib/demo-crawl.mjs uses each one).
    await ctx.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
    const problems = [];
    let where = "loading";
    await ctx.route("**/*", (route) => {
      const url = new URL(route.request().url());
      if (url.origin === origin || url.protocol === "data:" || url.protocol === "blob:") {
        return route.continue();
      }
      problems.push(`${where}: a request left the origin: ${url.href}`);
      return route.abort("blockedbyclient");
    });
    const page = await ctx.newPage();
    page.on("console", (msg) => {
      if (msg.type() === "error") {
        problems.push(`${where}: console error: ${msg.text()}`);
      }
    });
    page.on("pageerror", (err) => problems.push(`${where}: uncaught: ${err.message}`));
    page.on("requestfailed", (req) => {
      if (new URL(req.url()).origin === origin) {
        problems.push(`${where}: request failed: ${req.url()} (${req.failure()?.errorText})`);
      }
    });
    page.on("response", (res) => {
      if (res.status() >= 400) {
        problems.push(`${where}: ${res.status()} for ${res.url()}`);
      }
    });

    const manifest = JSON.parse(readFileSync(join(dir, "recording/manifest.json"), "utf8"));
    const topicData = async (name) => {
      const file = manifest.frames[0].topics[name];
      return file
        ? JSON.parse(readFileSync(join(dir, "recording/data", file), "utf8")).data
        : undefined;
    };
    const tracker = trackRequests(page);
    const site = {
      url: (route) => `${base}#${route}`,
      routeOf: (href) => (href.startsWith("#/") ? appRoute(href.slice(1)) : undefined),
      current: () => new URL(page.url()).hash.slice(1) || "/",
      settle: () => tracker.settle(),
    };
    let missesSeen = 0;
    const onCopy = (label, ref, problem) => {
      if (problem !== undefined) {
        problems.push(`${label}: copying the context of ${ref} failed: ${problem}`);
      }
    };
    const { routes, copied } = await crawl(
      page,
      site,
      await derivedRoutes(topicData),
      async (label) => {
        where = label;
        const misses = await page.evaluate(() => window.__dashDemo?.misses ?? null);
        if (misses === null) {
          problems.push(`${label}: the demo did not start (no window.__dashDemo)`);
          return;
        }
        for (const key of misses.slice(missesSeen)) {
          problems.push(`${label}: the recording has no answer for GET ${key}`);
        }
        missesSeen = misses.length;
        const lacks = await unfinished(page);
        if (lacks) {
          problems.push(`${label}: ${lacks}`);
        }
        console.log(`demo-smoke: ${label}`);
      },
      onCopy,
    );

    // A write: the toast carries the read-only message, and the page stays as it was.
    where = "/gitops [reconcile]";
    await page.goto(`${base}#/gitops`);
    await page.getByText("LIVE", { exact: true }).waitFor();
    await page.locator('button[title^="Reconcile "]').first().click();
    await page.getByText(READ_ONLY_TEXT).first().waitFor({ timeout: 10_000 });
    const lacks = await unfinished(page);
    if (lacks) {
      problems.push(`${where}: after the write, ${lacks}`);
    }
    console.log(`demo-smoke: ${where}: the read-only message is shown`);

    if (problems.length > 0) {
      throw new Error(`${problems.length} problem(s):\n${problems.join("\n")}`);
    }
    console.log(
      `demo-smoke: ok, ${routes.length} routes and ${copied.length} contexts copied from ${PREFIX} without a problem`,
    );
  } finally {
    clearTimeout(timer);
    await stop();
  }
}

main().catch((err) => {
  console.error(`demo-smoke: ${err.stack ?? err}`);
  exit(1);
});
