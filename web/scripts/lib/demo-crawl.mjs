// The route plan of the static demo, shared by the recorder (demo-export.mjs, against dash in
// demo mode) and the smoke test (demo-smoke.mjs, against the static build): both open the same
// routes and click the same tabs, so the smoke asks the recording for exactly what the SPA asked
// dash for. A site adapter hides the one difference, the routing: dash serves the SPA's routes as
// paths, the static demo keeps them in the URL fragment (src/demo/router.ts).

/** Every range a chart offers (SeriesRange in web/src/api/queries.ts). */
export const SERIES_RANGES = ["15m", "1h", "6h", "24h", "7d"];
/** Every period the pod logs offer (LogSince in web/src/api/queries.ts). */
export const LOG_SINCE = ["5m", "15m", "1h", "6h", "24h"];

/** The crawl refuses to run away: a plan longer than this is a bug in link following. */
const MAX_ROUTES = 400;
/** How long a route gets to settle: no request in flight for QUIET_MS, at most SETTLE_MS. */
const QUIET_MS = 400;
const SETTLE_MS = 15_000;

/** App routes the crawl never opens: the API, the sign-in flow and the server's own pages. */
const NOT_APP = /^\/(api|auth|oauth|login|setup|mcp|healthz|readyz)(\/|\?|$)/;

/**
 * The key a GET is recorded and looked up by, as src/demo/recording.ts computes it: the path, then
 * the query with its parameters sorted by name and value.
 */
export function recordingKey(pathname, search) {
  const params = [...new URLSearchParams(search)];
  params.sort(([ak, av], [bk, bv]) => (ak === bk ? cmp(av, bv) : cmp(ak, bk)));
  const query = new URLSearchParams(params).toString();
  return query ? `${pathname}?${query}` : pathname;
}

function cmp(a, b) {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** The SPA route an href of a page names, or undefined for anything that is not an app page. */
export function appRoute(href) {
  if (!href.startsWith("/") || href.startsWith("//")) {
    return undefined;
  }
  const route = href.split("#")[0];
  return NOT_APP.test(route) ? undefined : route;
}

const enc = encodeURIComponent;

/**
 * Object routes no list may link in full (a list can be filtered or cut short), derived from the
 * topic data as screenshots.mjs does for one pod. A topic the recording lacks adds nothing, so a
 * plugin that is not there yet costs nothing here.
 */
const DERIVED = [
  [
    "kubernetes.cluster",
    (d) => (d?.pods ?? []).map((p) => `/cluster/pods/${enc(p.namespace)}/${enc(p.name)}`),
  ],
  ["proxmox.vms", (d) => (d?.vms ?? []).filter((v) => !v.template).map((v) => `/vms/${v.vmid}`)],
  ["hardware.hosts", (d) => (d?.hosts ?? []).map((h) => `/hardware/${enc(h.name)}`)],
  ["hardware.machines", (d) => (d?.machines ?? []).map((m) => `/hardware/${enc(m.name)}`)],
  ["ssh.hosts", (d) => (d?.hosts ?? []).map((h) => `/hosts/${enc(h.name)}`)],
];

/** The derived object routes of the topics `data(name)` returns (undefined for a missing one). */
export async function derivedRoutes(data) {
  const routes = [];
  for (const [topic, paths] of DERIVED) {
    routes.push(...paths(await data(topic)));
  }
  return routes;
}

/**
 * Counts the page's requests in flight, so settle can wait until none is. Event streams never end
 * and are not counted.
 */
export function trackRequests(page) {
  const inflight = new Set();
  let last = Date.now();
  const done = (req) => {
    if (inflight.delete(req)) {
      last = Date.now();
    }
  };
  page.on("request", (req) => {
    if (req.resourceType() !== "eventsource") {
      inflight.add(req);
      last = Date.now();
    }
  });
  page.on("requestfinished", done);
  page.on("requestfailed", done);
  return {
    /** Resolves once no request was in flight for QUIET_MS, or after SETTLE_MS. */
    async settle() {
      const deadline = Date.now() + SETTLE_MS;
      await page
        .waitForFunction(() => !document.querySelector('[aria-busy="true"]'), null, {
          timeout: SETTLE_MS,
        })
        .catch(() => {});
      while (Date.now() < deadline && (inflight.size > 0 || Date.now() - last < QUIET_MS)) {
        await page.waitForTimeout(50);
      }
    },
  };
}

/**
 * What a settled page still lacks, or undefined: an empty main area, the status pill still
 * "Loading…" (the overview topic never arrived) or a panel still busy (its data never came).
 */
export async function unfinished(page) {
  // biome-ignore lint/performance/useDomNodeTextContent: blank means nothing rendered; the text of hidden nodes must not count.
  if ((await page.locator("main").innerText()).trim() === "") {
    return "the page is blank";
  }
  if (await page.getByText("Loading…", { exact: true }).isVisible()) {
    return "the status pill still says Loading… (no core.overview)";
  }
  if ((await page.locator('[aria-busy="true"]').count()) > 0) {
    return "a panel is still loading";
  }
  return undefined;
}

/** The label of the "Copy context for AI" control (web/src/components/copy-context.tsx). */
const COPY_CONTEXT = "Copy context for AI";
/** A page with more of them than this (an attention list) gets this many used. */
const MAX_COPIES_PER_PAGE = 10;
const TOAST_MS = 10_000;

/**
 * The contextRef and name a "Copy context for AI" button was rendered with, read from the props of
 * its React component: the ref decides the request (GET /api/v1/context?ref=…), so it is the key
 * a control is used once by, before anything is clicked. Undefined when React holds none.
 */
function contextProps(button) {
  return button.evaluate((el) => {
    const key = Object.keys(el).find((k) => k.startsWith("__reactFiber$"));
    for (let f = key ? el[key] : null; f; f = f.return) {
      const p = f.memoizedProps;
      if (p && typeof p.contextRef === "string") {
        return { ref: p.contextRef, name: String(p.name ?? "") };
      }
    }
    return undefined;
  });
}

/**
 * Uses each visible "Copy context for AI" control of the page once per crawl (`copied` holds the
 * refs used so far), at most MAX_COPIES_PER_PAGE here, in document order, and reports each result
 * with onCopy(ref, problem): problem is undefined when the success toast for the object came, the
 * error toast's text or a timeout otherwise. The page needs the clipboard permissions.
 */
async function copyContexts(page, site, route, copied, onCopy) {
  const buttons = page.getByRole("button", { name: COPY_CONTEXT, exact: true });
  let used = 0;
  for (let i = 0; i < (await buttons.count()) && used < MAX_COPIES_PER_PAGE; i++) {
    const button = buttons.nth(i);
    if (!(await button.isVisible()) || !(await button.isEnabled())) {
      continue;
    }
    const props = await contextProps(button);
    if (!props) {
      throw new Error(`${route}: a "${COPY_CONTEXT}" button without its contextRef`);
    }
    if (copied.has(props.ref)) {
      continue;
    }
    copied.add(props.ref);
    used++;
    await button.click();
    const toasts = page.locator("[data-sonner-toast]");
    // copiedMessage in copy-context.tsx: the name, then the logs it holds unless it has none.
    const name = props.name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const said = new RegExp(`^\\s*Copied context for ${name}(?: \\(|\\s*$)`);
    const ok = toasts.filter({ hasText: said }).first();
    const failed = toasts.filter({ hasText: "Copying context failed" }).first();
    const problem = await Promise.race([
      ok.waitFor({ timeout: TOAST_MS }).then(() => undefined),
      failed.waitFor({ timeout: TOAST_MS }).then(async () => (await failed.textContent()) ?? ""),
    ]).catch(() => `no toast within ${TOAST_MS / 1000} s`);
    await onCopy(props.ref, problem);
    // A control inside a link would have left the page; the rest of the page is still to do.
    if (site.current() !== route) {
      await page.goto(site.url(route));
      await site.settle();
    }
  }
}

/**
 * Opens every route of the plan in page and calls visit(label) on each, then on each tab clicked
 * there: "/" and the seeds (the derived object routes) first, then every app link a visited page
 * shows. Before each visit it uses every "Copy context for AI" control on screen not used yet
 * (copyContexts), reporting each with onCopy(label, ref, problem). site adapts the routing:
 * url(route) is the address to open, routeOf(href) the route an href names (undefined for none),
 * current() the route on screen and settle() waits for the page to go quiet. Returns the routes
 * visited and the refs whose context was copied.
 */
export async function crawl(page, site, seeds, visit, onCopy) {
  const queue = ["/", ...seeds];
  const seen = new Set();
  const copied = new Set();
  // A list links an object with its own filters (`/cluster/pods/a/b?ns=a`); the object's sheet is
  // the same whatever the list behind it, so an object route is opened once, as derived.
  const objects = new Set(seeds);
  const enqueue = async () => {
    const hrefs = await page.$$eval("a[href]", (as) => as.map((a) => a.getAttribute("href")));
    for (const href of hrefs) {
      const route = site.routeOf(href);
      if (route !== undefined && !seen.has(route) && !objects.has(route.split("?")[0])) {
        queue.push(route);
      }
    }
  };
  const copy = (label, route) =>
    copyContexts(page, site, route, copied, (ref, problem) => onCopy(label, ref, problem));
  while (queue.length > 0) {
    const route = queue.shift();
    if (seen.has(route)) {
      continue;
    }
    seen.add(route);
    if (seen.size > MAX_ROUTES) {
      throw new Error(`the crawl passed ${MAX_ROUTES} routes; is a link generating new ones?`);
    }
    await page.goto(site.url(route));
    await page.getByText("LIVE", { exact: true }).waitFor({ timeout: 30_000 });
    await site.settle();
    await copy(route, route);
    await visit(route);
    await enqueue();
    // Every tab of the page once, in order; a tab that is selected already is what was visited.
    for (let i = 0; i < (await page.getByRole("tab").count()); i++) {
      const tab = page.getByRole("tab").nth(i);
      if ((await tab.getAttribute("aria-selected")) === "true" || !(await tab.isEnabled())) {
        continue;
      }
      await tab.click();
      await site.settle();
      const at = site.current();
      seen.add(at);
      const label = `${at} [tab ${(await tab.textContent()).trim()}]`;
      await copy(label, at);
      await visit(label);
      await enqueue();
    }
  }
  return { routes: [...seen], copied: [...copied] };
}
