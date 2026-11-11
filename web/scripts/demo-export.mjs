// The static demo's recording (docs/development.md, "Static demo"), from dash in demo mode.
// `make demo` builds the SPA and dash and runs:
//
//   DASH_DEMO_CLOCK=2026-01-15T12:00:00Z node scripts/demo-export.mjs <dash binary> <out dir>
//
// Frames: for each of DEMO_FRAMES instants (default 24) DEMO_STEP_SECONDS apart (default 5) from
// DASH_DEMO_CLOCK, a dash started at that clock, every topic read once. REST: Playwright crawls
// the real SPA served by a dash at DASH_DEMO_CLOCK along the plan of lib/demo-crawl.mjs and keeps
// every successful GET under /api/ the SPA made, the context bundles of every "Copy context for
// AI" control the crawl uses included; then, for each chart and log query it recorded,
// the same query for every other range or period the UI offers, fetched directly. Every file is
// stored once under its content's hash; the manifest maps requests and frames to them. Two runs
// with the same binary and clock write the same bytes: the demo world stands still at its clock,
// the browser's Date is fixed, the viewport, timezone and locale are pinned, and no request
// leaves dash. The output dir is replaced only once the whole recording succeeded.
//
// Needs Playwright's Chromium once: `npx playwright install chromium`.
import { createHash } from "node:crypto";
import {
  cpSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { chromium } from "playwright";
import { exit, freePorts, onSignals, startDash, stopAll } from "./lib/demo.mjs";
import {
  appRoute,
  crawl,
  derivedRoutes,
  LOG_SINCE,
  recordingKey,
  SERIES_RANGES,
  trackRequests,
  unfinished,
} from "./lib/demo-crawl.mjs";

const [bin, outArg] = process.argv.slice(2);
const clock = process.env.DASH_DEMO_CLOCK;
const frameCount = Number(process.env.DEMO_FRAMES ?? 24);
const stepSeconds = Number(process.env.DEMO_STEP_SECONDS ?? 5);
if (!bin || !outArg || !clock || Number.isNaN(Date.parse(clock))) {
  console.error("usage: DASH_DEMO_CLOCK=<RFC 3339> node scripts/demo-export.mjs <dash> <out dir>");
  process.exit(2);
}
if (!(Number.isInteger(frameCount) && frameCount > 0 && stepSeconds > 0)) {
  console.error(
    "demo-export: DEMO_FRAMES must be a positive integer and DEMO_STEP_SECONDS positive",
  );
  process.exit(2);
}
const outDir = resolve(outArg);
const start = Date.parse(clock);

/** The core topics, before every plugin's: the order dash's stream sends them in. */
const CORE_TOPICS = ["core.plugins", "core.overview", "core.approvals"];
const VIEWPORT = { width: 1440, height: 900 };

/** The recording's files by name, each stored once whatever refers to it. */
const files = new Map();

/** Stores body (a Buffer) under its hash; returns the file name. */
function store(body, json) {
  const name = `${createHash("sha256").update(body).digest("hex").slice(0, 24)}${json ? ".json" : ".txt"}`;
  files.set(name, body);
  return name;
}

/** RFC 3339 in UTC, to the second, as the clock is given. */
function iso(ms) {
  return new Date(ms).toISOString().replace(/\.000Z$/, "Z");
}

/** The topic names of a core.plugins envelope, in the order the stream sends them. */
function topicNames(pluginsEnvelope) {
  const names = [...CORE_TOPICS];
  for (const p of pluginsEnvelope.data?.plugins ?? []) {
    for (const t of p.topics ?? []) {
      if (!names.includes(t)) {
        names.push(t);
      }
    }
  }
  return names;
}

async function getTopic(origin, name) {
  const res = await fetch(`${origin}/api/v1/topics/${name}`);
  if (res.status === 404) {
    return undefined; // a topic nobody published (core.approvals without approvals)
  }
  if (!res.ok) {
    throw new Error(`GET /api/v1/topics/${name}: ${res.status}`);
  }
  return Buffer.from(await res.arrayBuffer());
}

/**
 * What dash's hub compares to decide whether a publish is news: the envelope without its time and
 * its sources' check times (internal/hub dedups on data and sources, ignoring checkedAt).
 */
function payload(body) {
  const env = JSON.parse(body.toString());
  return JSON.stringify({
    ...env,
    updatedAt: undefined,
    sources: (env.sources ?? []).map((s) => ({ ...s, checkedAt: undefined })),
  });
}

/** How long a fresh dash gets for its topics to settle, and how often they are read meanwhile. */
const SETTLE_TOPICS_MS = 20_000;
const READ_EVERY_MS = 300;

/** Every published topic of the dash at origin, by name, in the order the stream sends them. */
async function readTopics(origin) {
  const topics = new Map();
  const plugins = await getTopic(origin, "core.plugins");
  if (plugins) {
    for (const name of topicNames(JSON.parse(plugins.toString()))) {
      const body = name === "core.plugins" ? plugins : await getTopic(origin, name);
      if (body) {
        topics.set(name, body);
      }
    }
  }
  return topics;
}

const sameTopics = (a, b) =>
  a.size === b.size && [...a].every(([name, body]) => b.get(name)?.equals(body));

/**
 * The topics of a dash that just became ready, once they settled: the plugins' twins publish
 * after /readyz, and the overview is composed from what they published, so a single early read
 * would catch them half done. Settled is the overview there and three reads in a row alike (the
 * demo world stands still at its clock).
 */
async function settledTopics(origin) {
  const deadline = Date.now() + SETTLE_TOPICS_MS;
  const reads = [];
  for (;;) {
    reads.unshift(await readTopics(origin));
    reads.length = Math.min(reads.length, 3);
    if (
      reads.length === 3 &&
      reads[0].has("core.overview") &&
      sameTopics(reads[0], reads[1]) &&
      sameTopics(reads[1], reads[2])
    ) {
      return reads[0];
    }
    if (Date.now() > deadline) {
      throw new Error(`the topics did not settle within ${SETTLE_TOPICS_MS / 1000} s`);
    }
    await new Promise((r) => setTimeout(r, READ_EVERY_MS));
  }
}

/**
 * One frame: every topic of a dash started at `at`. A topic whose payload equals prev's keeps
 * prev's file, as the hub keeps its stored event, so the player does not send it again.
 */
async function recordFrame(at, prev) {
  const [apiPort, healthPort] = await freePorts(2);
  const origin = `http://127.0.0.1:${apiPort}`;
  await startDash(bin, apiPort, healthPort, { DASH_DEMO_CLOCK: iso(at) });
  try {
    const topics = {};
    for (const [name, body] of await settledTopics(origin)) {
      const before = prev?.topics[name];
      topics[name] =
        before && payload(files.get(before)) === payload(body) ? before : store(body, true);
    }
    return { at: iso(at), topics };
  } finally {
    await stopAll();
  }
}

/** A browser context fixed for repeatable answers, allowed to reach origin only. */
async function newContext(browser, origin) {
  const ctx = await browser.newContext({
    viewport: VIEWPORT,
    reducedMotion: "reduce",
    colorScheme: "dark",
    timezoneId: "UTC",
    locale: "en-US",
    serviceWorkers: "block",
  });
  await ctx.clock.setFixedTime(new Date(clock));
  // "Copy context for AI" writes to the clipboard (lib/demo-crawl.mjs uses each one).
  await ctx.grantPermissions(["clipboard-read", "clipboard-write"], { origin });
  await ctx.route("**/*", (route) => {
    const url = new URL(route.request().url());
    if (url.origin === origin || url.protocol === "data:" || url.protocol === "blob:") {
      return route.continue();
    }
    return route.abort("blockedbyclient");
  });
  return ctx;
}

/** The URL with param set to each of values, other than the one it has. */
function variants(url, param, values) {
  const out = [];
  for (const v of values) {
    if (url.searchParams.get(param) !== v) {
      const u = new URL(url);
      u.searchParams.set(param, v);
      out.push(u);
    }
  }
  return out;
}

/** The REST answers: every GET under /api/ the SPA made on the crawl, plus the range variants. */
async function recordREST(frame) {
  const [apiPort, healthPort] = await freePorts(2);
  const origin = `http://127.0.0.1:${apiPort}`;
  const rest = new Map();
  const failed = new Map();
  let browser;
  try {
    await startDash(bin, apiPort, healthPort, { DASH_DEMO_CLOCK: clock });
    await settledTopics(origin);
    browser = await chromium.launch();
    const ctx = await newContext(browser, origin);
    const page = await ctx.newPage();
    const pending = [];
    page.on("response", (res) => {
      const url = new URL(res.url());
      const req = res.request();
      if (url.origin !== origin || !url.pathname.startsWith("/api/") || req.method() !== "GET") {
        return;
      }
      if (req.resourceType() === "eventsource") {
        return;
      }
      const key = recordingKey(url.pathname, url.search);
      if (!res.ok()) {
        failed.set(key, res.status());
        return;
      }
      const json = (res.headers()["content-type"] ?? "").includes("application/json");
      pending.push(
        res.body().then(
          (body) => rest.set(key, store(body, json)),
          (err) => failed.set(key, String(err)),
        ),
      );
    });
    const errors = [];
    page.on("pageerror", (err) => errors.push(String(err)));
    const tracker = trackRequests(page);
    const site = {
      url: (route) => origin + route,
      routeOf: appRoute,
      current: () => {
        const u = new URL(page.url());
        return u.pathname + u.search;
      },
      settle: () => tracker.settle(),
    };
    const topicData = async (name) => {
      const file = frame.topics[name];
      return file ? JSON.parse(files.get(file).toString()).data : undefined;
    };
    const unfinishedPages = [];
    const { routes, copied } = await crawl(
      page,
      site,
      await derivedRoutes(topicData),
      async (label) => {
        const lacks = await unfinished(page);
        if (lacks) {
          unfinishedPages.push(`${label}: ${lacks}`);
        }
        console.log(`demo-export: ${label}`);
      },
      (label, ref, problem) => {
        if (problem !== undefined) {
          unfinishedPages.push(`${label}: copying the context of ${ref} failed: ${problem}`);
        }
      },
    );
    await Promise.all(pending);
    if (errors.length > 0 || unfinishedPages.length > 0) {
      throw new Error(
        `the crawl of dash went wrong:\n${[...errors, ...unfinishedPages].join("\n")}`,
      );
    }
    // Every other range of each chart and every other period of each log query.
    const extra = [];
    for (const key of rest.keys()) {
      const url = new URL(key, origin);
      if (url.pathname === "/api/v1/series") {
        extra.push(...variants(url, "range", SERIES_RANGES));
      } else if (url.pathname === "/api/v1/plugins/victorialogs/logs") {
        extra.push(...variants(url, "since", LOG_SINCE));
      }
    }
    for (const url of extra) {
      const key = recordingKey(url.pathname, url.search);
      if (rest.has(key)) {
        continue;
      }
      const res = await fetch(url, { headers: { Accept: "application/json" } });
      if (!res.ok) {
        throw new Error(`GET ${key}: ${res.status}`);
      }
      const json = (res.headers.get("content-type") ?? "").includes("application/json");
      rest.set(key, store(Buffer.from(await res.arrayBuffer()), json));
    }
    for (const [key, status] of failed) {
      if (!rest.has(key)) {
        console.warn(
          `demo-export: the SPA's GET ${key} answered ${status}; the demo answers it 404`,
        );
      }
    }
    await ctx.close();
    return { rest, routes: routes.length, contexts: copied.length };
  } finally {
    await browser?.close();
    await stopAll();
  }
}

/** Replaces dir with the directory from: a rename, or a copy when they are on different filesystems. */
function replaceDir(from, dir) {
  rmSync(dir, { recursive: true, force: true });
  mkdirSync(dirname(dir), { recursive: true });
  try {
    renameSync(from, dir);
  } catch (err) {
    if (err.code !== "EXDEV") {
      throw err;
    }
    cpSync(from, dir, { recursive: true });
    rmSync(from, { recursive: true, force: true });
  }
}

function sizeOf(dir) {
  let total = 0;
  for (const name of readdirSync(dir, { recursive: true })) {
    const st = statSync(join(dir, name));
    if (st.isFile()) {
      total += st.size;
    }
  }
  return total;
}

const kib = (n) => `${(n / 1024).toFixed(1)} KiB`;

async function main() {
  const tmp = mkdtempSync(join(tmpdir(), "dash-demo-recording-"));
  onSignals(async () => {
    try {
      await stopAll();
    } finally {
      rmSync(tmp, { recursive: true, force: true });
    }
  });
  try {
    const frames = [];
    for (let i = 0; i < frameCount; i++) {
      frames.push(await recordFrame(start + i * stepSeconds * 1000, frames.at(-1)));
    }
    const frameFiles = new Set(frames.flatMap((f) => Object.values(f.topics)));
    console.log(`demo-export: ${frames.length} frames, ${frameFiles.size} distinct topic files`);
    const { rest, routes, contexts } = await recordREST(frames[0]);

    const manifest = {
      version: 1,
      stepSeconds,
      frames,
      rest: Object.fromEntries([...rest].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))),
    };
    mkdirSync(join(tmp, "data"));
    for (const name of [...files.keys()].sort()) {
      writeFileSync(join(tmp, "data", name), files.get(name));
    }
    writeFileSync(join(tmp, "manifest.json"), `${JSON.stringify(manifest, null, 1)}\n`);
    replaceDir(tmp, outDir);

    const restFiles = new Set(rest.values());
    console.log(
      [
        `demo-export: ${outDir}`,
        `  routes crawled:     ${routes} (${contexts} contexts copied)`,
        `  frames:             ${frames.length} (${stepSeconds} s apart, from ${frames[0].at})`,
        `  recorded responses: ${rest.size} GETs (${restFiles.size} distinct files)`,
        `  files:              ${files.size}`,
        `  size:               ${kib(sizeOf(outDir))} (manifest ${kib(statSync(join(outDir, "manifest.json")).size)})`,
      ].join("\n"),
    );
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(`demo-export: ${err.stack ?? err}`);
  exit(1);
});
