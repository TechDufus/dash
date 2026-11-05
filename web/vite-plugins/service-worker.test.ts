import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, type Mock, vi } from "vitest";
import {
  BUILD_PLACEHOLDER,
  buildHash,
  PRECACHE_PLACEHOLDER,
  type PublicFile,
  precacheList,
  publicFiles,
  renderServiceWorker,
  serviceWorker,
  workerFor,
} from "./service-worker.ts";

const WEB = join(import.meta.dirname, "..");
const SOURCE = readFileSync(join(WEB, "sw", "sw.js"), "utf8");
const PUBLIC = join(WEB, "public");

const pub = (name: string, text = name): PublicFile => ({
  name,
  bytes: new TextEncoder().encode(text),
});

describe("precacheList", () => {
  it("holds the hashed assets and the public files, sorted, never index.html or a map", () => {
    const list = precacheList(
      ["index.html", "assets/index-AbC.js", "assets/index-AbC.js.map", "assets/app-1.css"],
      [pub("offline.html"), pub("icons/icon-192.png")],
    );
    expect(list).toEqual([
      "/assets/app-1.css",
      "/assets/index-AbC.js",
      "/icons/icon-192.png",
      "/offline.html",
    ]);
  });

  it("refuses a public file outside offline.html and the PNG icons", () => {
    expect(() => precacheList([], [pub("manifest.webmanifest")])).toThrow(
      /service-worker: not precacheable: \/manifest\.webmanifest/,
    );
  });
});

describe("buildHash", () => {
  const list = ["/assets/a-1.js", "/offline.html"];
  const files = [pub("offline.html", "v1")];
  const base = buildHash(SOURCE, list, files);

  it("is stable for the same build and 16 hex characters", () => {
    expect(base).toMatch(/^[0-9a-f]{16}$/);
    expect(buildHash(SOURCE, [...list], [pub("offline.html", "v1")])).toBe(base);
  });

  it("changes with the worker's source, the list or a public file's bytes", () => {
    expect(buildHash(`${SOURCE}\n`, list, files)).not.toBe(base);
    expect(buildHash(SOURCE, ["/assets/a-2.js", "/offline.html"], files)).not.toBe(base);
    expect(buildHash(SOURCE, list, [pub("offline.html", "v2")])).not.toBe(base);
  });
});

describe("renderServiceWorker", () => {
  it("fills in the build hash and the list", () => {
    const out = renderServiceWorker(SOURCE, ["/assets/$&-1.js"], "0123456789abcdef");
    expect(out).toContain('const BUILD = "0123456789abcdef";');
    expect(out).toContain('const PRECACHE = ["/assets/$&-1.js"];');
    expect(out).not.toContain("__DASH_SW_");
  });

  it.each([BUILD_PLACEHOLDER, PRECACHE_PLACEHOLDER])("fails when %s is missing", (ph) => {
    expect(() => renderServiceWorker(SOURCE.replace(ph, "x"), [], "b")).toThrow(
      /service-worker: web\/sw\/sw\.js must hold/,
    );
  });
});

describe("publicFiles", () => {
  let dir: string;
  beforeEach(() => {
    dir = mkdtempSync(join(tmpdir(), "dash-sw-test-"));
  });
  afterEach(() => {
    rmSync(dir, { recursive: true, force: true });
  });

  it("reads the real offline page and the four icons", () => {
    expect(
      publicFiles(PUBLIC)
        .map((f) => f.name)
        .sort(),
    ).toEqual([
      "icons/apple-touch-icon-180.png",
      "icons/icon-192.png",
      "icons/icon-512.png",
      "icons/maskable-512.png",
      "offline.html",
    ]);
  });

  it("fails without icons or without the offline page", () => {
    mkdirSync(join(dir, "icons"));
    writeFileSync(join(dir, "offline.html"), "x");
    expect(() => publicFiles(dir)).toThrow(/public\/icons holds no PNG icon/);
    writeFileSync(join(dir, "icons", "i.png"), "x");
    expect(publicFiles(dir)).toHaveLength(2);
    rmSync(join(dir, "offline.html"));
    expect(() => publicFiles(dir)).toThrow(/cannot read public\/offline\.html/);
  });
});

describe("serviceWorker plugin", () => {
  it("emits sw.js with the bundle's assets in a build only", () => {
    const plugin = serviceWorker();
    expect(plugin.apply).toBe("build");
    (plugin.configResolved as (c: { publicDir: string }) => void)({ publicDir: PUBLIC });
    const emitFile = vi.fn();
    const bundle = {
      "index.html": { fileName: "index.html" },
      "assets/index-AbC.js": { fileName: "assets/index-AbC.js" },
    };
    (plugin.generateBundle as (this: unknown, o: unknown, b: unknown) => void).call(
      { emitFile },
      {},
      bundle,
    );
    expect(emitFile).toHaveBeenCalledTimes(1);
    const file = emitFile.mock.calls[0]?.[0] as { fileName: string; source: string };
    expect(file.fileName).toBe("sw.js");
    expect(file.source).toContain('"/assets/index-AbC.js"');
    expect(file.source).toContain('"/offline.html"');
    expect(file.source).not.toContain('index.html"');
  });

  it("refuses a bundle that already has an sw.js", () => {
    expect(() => workerFor(SOURCE, { "sw.js": { fileName: "sw.js" } }, PUBLIC)).toThrow(
      /already has an sw\.js/,
    );
  });
});

// The worker itself, run against fakes of its globals: what it answers and what it leaves alone.
// The fake Cache Storage keys entries by full URL (so a match without ignoreSearch misses a query
// string), its addAll is all or nothing like the browser's, and every write is recorded: after
// each test, the cache holds nothing but precached files and nothing was written after install.
describe("sw.js", () => {
  const ORIGIN = "https://dash.example.com";
  const PRECACHE = ["/assets/index-AbC.js", "/icons/icon-192.png", "/offline.html"];
  const abs = (r: string | { url: string }) =>
    new URL(typeof r === "string" ? r : r.url, ORIGIN).href;
  const bare = (u: string) => u.replace(/[?#].*$/, "");

  type Listener = (event: unknown) => void;
  interface Worker {
    on: Map<string, Listener>;
    store: Map<string, Map<string, Response>>;
    writes: string[]; // "addAll|add|put <url>", in order
    installed: boolean;
    unreachable: Set<string>; // paths the cache cannot fetch (a 404 at install)
    fetch: Mock;
    skipWaiting: Mock;
    claim: Mock;
    showNotification: Mock;
    openWindow: Mock;
  }

  let current: Worker | undefined;

  function load(): Worker {
    const store = new Map<string, Map<string, Response>>();
    const writes: string[] = [];
    const unreachable = new Set<string>();
    const fill = (url: string) => {
      const path = new URL(abs(url)).pathname;
      if (unreachable.has(path)) throw new TypeError(`Request failed: ${path}`);
      return new Response(`cached ${path}`);
    };
    const caches = {
      open: async (name: string) => {
        const entries = store.get(name) ?? new Map<string, Response>();
        store.set(name, entries);
        return {
          addAll: async (urls: string[]) => {
            const got = urls.map((u) => [abs(u), fill(u)] as const); // throws before any write
            for (const [u, res] of got) {
              writes.push(`addAll ${u}`);
              entries.set(u, res);
            }
          },
          add: async (u: string) => {
            const res = fill(u);
            writes.push(`add ${abs(u)}`);
            entries.set(abs(u), res);
          },
          put: async (r: string | { url: string }, res: Response) => {
            writes.push(`put ${abs(r)}`);
            entries.set(abs(r), res);
          },
          match: async (r: string | { url: string }, opts?: { ignoreSearch?: boolean }) => {
            const key = abs(r);
            const hit = opts?.ignoreSearch
              ? [...entries].find(([u]) => bare(u) === bare(key))?.[1]
              : entries.get(key);
            return hit?.clone();
          },
        };
      },
      keys: async () => [...store.keys()],
      delete: async (name: string) => store.delete(name),
    };
    const on = new Map<string, Listener>();
    const w: Worker = {
      on,
      store,
      writes,
      installed: false,
      unreachable,
      fetch: vi.fn(),
      skipWaiting: vi.fn(async () => {}),
      claim: vi.fn(async () => {}),
      showNotification: vi.fn(async () => {}),
      openWindow: vi.fn(async () => null),
    };
    const self = {
      addEventListener: (type: string, fn: Listener) => on.set(type, fn),
      location: new URL(`${ORIGIN}/sw.js`),
      skipWaiting: w.skipWaiting,
      clients: { claim: w.claim, openWindow: w.openWindow },
      registration: { showNotification: w.showNotification },
    };
    new Function("self", "caches", "fetch", renderServiceWorker(SOURCE, PRECACHE, "b1"))(
      self,
      caches,
      w.fetch,
    );
    current = w;
    return w;
  }

  /** Dispatches an extendable event; returns the promise it passed to waitUntil. */
  function dispatch(w: Worker, type: string, extra: object = {}): Promise<unknown> {
    let wait: Promise<unknown> = Promise.resolve();
    w.on.get(type)?.({
      ...extra,
      waitUntil: (p: Promise<unknown>) => {
        wait = p;
      },
    });
    return wait;
  }

  /** Installs the worker; from here on it may write nothing (the afterEach check). */
  async function install(w: Worker) {
    await dispatch(w, "install");
    w.writes.length = 0;
    w.installed = true;
  }

  /** Deletes path from the worker's cache, as an eviction would (not a write of the worker). */
  function evict(w: Worker, path: string) {
    expect(w.store.get("dash-b1")?.delete(abs(path))).toBe(true);
  }

  /** Dispatches a fetch event; returns the response the worker gave, or undefined for none. */
  async function request(w: Worker, url: string, init: { method?: string; mode?: string } = {}) {
    let answer: Promise<Response> | undefined;
    w.on.get("fetch")?.({
      request: { url: abs(url), method: init.method ?? "GET", mode: init.mode ?? "cors" },
      respondWith: (p: Promise<Response>) => {
        answer = p;
      },
    });
    return answer === undefined ? undefined : await answer;
  }

  // The worker's invariant: after install it never writes to Cache Storage, and nothing but the
  // precache list is ever in it.
  afterEach(() => {
    const w = current;
    current = undefined;
    if (!w?.installed) return;
    expect(w.writes).toEqual([]);
    const urls = [...w.store.values()].flatMap((entries) => [...entries.keys()]);
    expect(urls.filter((u) => !PRECACHE.map(abs).includes(u))).toEqual([]);
  });

  it("precaches exactly its list on install, then takes over", async () => {
    const w = load();
    await dispatch(w, "install");
    expect([...w.store.keys()]).toEqual(["dash-b1"]);
    expect([...(w.store.get("dash-b1")?.keys() ?? [])].sort()).toEqual(PRECACHE.map(abs));
    expect(w.skipWaiting).toHaveBeenCalledTimes(1);
  });

  it("fails its install, and never takes over, when a precached file cannot be fetched", async () => {
    const w = load();
    w.unreachable.add("/icons/icon-192.png");
    await expect(dispatch(w, "install")).rejects.toThrow(/icon-192/);
    expect(w.skipWaiting).not.toHaveBeenCalled();
    expect(w.writes).toEqual([]);
  });

  it("deletes every other cache on activate", async () => {
    const w = load();
    w.store.set("dash-old", new Map());
    w.store.set("something-else", new Map());
    await install(w);
    await dispatch(w, "activate");
    expect([...w.store.keys()]).toEqual(["dash-b1"]);
    expect(w.claim).toHaveBeenCalledTimes(1);
  });

  it.each([
    "/api/v1/stream?topics=core.overview",
    "/api",
    "/oauth/token",
    "/mcp",
    "/mcp/x",
    "/auth/callback",
    "/login",
    "/login/x",
    "/setup",
    "/.well-known/oauth-protected-resource/api",
  ])("never answers %s, as a navigation or not", async (path) => {
    const w = load();
    await install(w);
    expect(await request(w, path)).toBeUndefined();
    expect(await request(w, path, { mode: "navigate" })).toBeUndefined();
    expect(w.fetch).not.toHaveBeenCalled();
  });

  // A callback's code is good for one exchange: the browser sends the navigation itself, once.
  it("leaves a sign-in callback navigation to the browser", async () => {
    const w = load();
    await install(w);
    w.fetch.mockResolvedValue(new Response("relayed"));
    const url = "/auth/callback?code=abc&state=xyz";
    expect(await request(w, url, { mode: "navigate" })).toBeUndefined();
    expect(w.fetch).not.toHaveBeenCalled();
  });

  it("never answers another origin, a write, or a same-origin file it did not precache", async () => {
    const w = load();
    await install(w);
    expect(await request(w, "https://cdn.example.com/assets/index-AbC.js")).toBeUndefined();
    expect(await request(w, "/assets/index-AbC.js", { method: "POST" })).toBeUndefined();
    expect(await request(w, "/manifest.webmanifest")).toBeUndefined();
    expect(await request(w, "/assets/other-1.js")).toBeUndefined();
    expect(w.fetch).not.toHaveBeenCalled();
  });

  it("answers a precached file from the cache, with or without a query string", async () => {
    const w = load();
    await install(w);
    for (const url of ["/assets/index-AbC.js", "/assets/index-AbC.js?v=2"]) {
      expect(await (await request(w, url))?.text()).toBe("cached /assets/index-AbC.js");
    }
    expect(w.fetch).not.toHaveBeenCalled();
  });

  it("fetches a precached file the cache lost from the network, and keeps nothing", async () => {
    const w = load();
    await install(w);
    evict(w, "/assets/index-AbC.js");
    w.fetch.mockResolvedValue(new Response("from the network"));
    expect(await (await request(w, "/assets/index-AbC.js"))?.text()).toBe("from the network");
    expect(w.fetch).toHaveBeenCalledTimes(1);
  });

  it("sends navigations to the network first, and keeps nothing", async () => {
    const w = load();
    await install(w);
    w.fetch.mockResolvedValue(new Response("live document"));
    const res = await request(w, "/alerts", { mode: "navigate" });
    expect(await res?.text()).toBe("live document");
    expect(w.fetch).toHaveBeenCalledTimes(1);
  });

  it("answers a navigation that fails with the cached offline page", async () => {
    const w = load();
    await install(w);
    w.fetch.mockRejectedValue(new TypeError("Failed to fetch"));
    const res = await request(w, "/cluster/pods", { mode: "navigate" });
    expect(await res?.text()).toBe("cached /offline.html");
  });

  it("answers a failed navigation with a network error when the offline page is gone", async () => {
    const w = load();
    await install(w);
    evict(w, "/offline.html");
    w.fetch.mockRejectedValue(new TypeError("Failed to fetch"));
    const res = await request(w, "/alerts", { mode: "navigate" });
    expect(res?.type).toBe("error");
  });

  it("shows a notification for every push and opens dash on a tap", async () => {
    const w = load();
    await install(w);
    await dispatch(w, "push");
    expect(w.showNotification).toHaveBeenCalledExactlyOnceWith("dash", {
      icon: "/icons/icon-192.png",
    });
    const close = vi.fn();
    await dispatch(w, "notificationclick", { notification: { close } });
    expect(close).toHaveBeenCalledTimes(1);
    expect(w.openWindow).toHaveBeenCalledExactlyOnceWith("/");
  });
});
