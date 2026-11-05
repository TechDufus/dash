import { createHash } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { Plugin } from "vite";

// Emits dist/sw.js (P7.6) from the hand-written web/sw/sw.js with its two placeholders filled in
// from the build itself: the precache list (every emitted file under assets/, which Vite names by
// content hash, plus /offline.html and the PNG icons from public/) and a build hash over the
// worker's source, that list and the public files' bytes. A deploy that changes any of them
// changes sw.js, and the browser installs the new worker; one that changes none leaves it alone.
// index.html is never precached: it carries a per-response CSP nonce. The list comes from the
// bundle rather than from build.manifest, which would also ship .vite/manifest.json in dist.
//
// It FAILS LOUDLY: a source without its placeholders, a missing offline page or icons, or a
// precache entry outside /assets/*, /offline.html and /icons/*.png breaks the build.

export const BUILD_PLACEHOLDER = '"__DASH_SW_BUILD__"';
export const PRECACHE_PLACEHOLDER = "/* __DASH_SW_PRECACHE__ */ []";
const SOURCE = join(import.meta.dirname, "..", "sw", "sw.js");
const ALLOWED = /^\/(?:assets\/[^/]+|offline\.html|icons\/[^/]+\.png)$/;

/** A file of public/ that the worker precaches: its path below public/ and its bytes. */
export interface PublicFile {
  name: string;
  bytes: Uint8Array;
}

/** The public files the worker precaches: offline.html and every PNG in icons/. */
export function publicFiles(publicDir: string): PublicFile[] {
  if (!publicDir) {
    throw new Error("service-worker: the build has no public dir (offline.html, icons/)");
  }
  const read = (name: string): PublicFile => {
    try {
      return { name, bytes: readFileSync(join(publicDir, name)) };
    } catch (err) {
      throw new Error(`service-worker: cannot read public/${name}: ${err}`);
    }
  };
  let icons: string[];
  try {
    icons = readdirSync(join(publicDir, "icons")).filter((f) => f.endsWith(".png"));
  } catch (err) {
    throw new Error(`service-worker: cannot list public/icons: ${err}`);
  }
  if (icons.length === 0) {
    throw new Error("service-worker: public/icons holds no PNG icon");
  }
  return [read("offline.html"), ...icons.map((f) => read(`icons/${f}`))];
}

/** The sorted precache URLs: the bundle's files under assets/ (no source maps) and the public files. */
export function precacheList(bundleFiles: string[], pub: PublicFile[]): string[] {
  const urls = [
    ...bundleFiles.filter((f) => f.startsWith("assets/") && !f.endsWith(".map")),
    ...pub.map((p) => p.name),
  ].map((f) => `/${f}`);
  const bad = urls.filter((u) => !ALLOWED.test(u));
  if (bad.length > 0) {
    throw new Error(`service-worker: not precacheable: ${bad.join(", ")}`);
  }
  return [...new Set(urls)].sort();
}

/** 16 hex characters of sha256 over the worker's source, the precache list and the public files. */
export function buildHash(source: string, precache: string[], pub: PublicFile[]): string {
  const h = createHash("sha256").update(source).update("\0").update(JSON.stringify(precache));
  for (const p of [...pub].sort((a, b) => (a.name < b.name ? -1 : 1))) {
    h.update("\0").update(p.name).update("\0").update(p.bytes);
  }
  return h.digest("hex").slice(0, 16);
}

/** The worker's source with the build hash and the precache list in place of the placeholders. */
export function renderServiceWorker(source: string, precache: string[], build: string): string {
  for (const ph of [BUILD_PLACEHOLDER, PRECACHE_PLACEHOLDER]) {
    if (source.split(ph).length !== 2) {
      throw new Error(`service-worker: web/sw/sw.js must hold ${ph} exactly once`);
    }
  }
  return source
    .replace(BUILD_PLACEHOLDER, () => JSON.stringify(build))
    .replace(PRECACHE_PLACEHOLDER, () => JSON.stringify(precache));
}

/** The worker for this bundle (file name → output; only fileName is read): web/sw/sw.js rendered. */
export function workerFor(
  source: string,
  bundle: Record<string, { fileName: string }>,
  publicDir: string,
): string {
  if ("sw.js" in bundle) {
    throw new Error("service-worker: the bundle already has an sw.js");
  }
  const pub = publicFiles(publicDir);
  const precache = precacheList(
    Object.values(bundle).map((f) => f.fileName),
    pub,
  );
  return renderServiceWorker(source, precache, buildHash(source, precache, pub));
}

export function serviceWorker(): Plugin {
  let publicDir = "";
  return {
    name: "dash:service-worker",
    apply: "build",
    configResolved(config) {
      publicDir = config.publicDir;
    },
    generateBundle(_options, bundle) {
      const source = workerFor(readFileSync(SOURCE, "utf8"), bundle, publicDir);
      this.emitFile({ type: "asset", fileName: "sw.js", source });
    },
  };
}
