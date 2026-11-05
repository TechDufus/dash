// Run after `vite build`: every lazy page must be its own chunk, and the entry chunk must not
// contain the page's code (epic P2.7: "each module's page is its own chunk"). It also checks that
// the build is CSP-ready (a strict `style-src`/`script-src`/`font-src` needs no 'unsafe-inline' and
// no `data:`): no `url(data:` in built CSS, no inline <script> or <style> in index.html, and the
// `csp-nonce` meta the server templates. And the PWA files: the manifest and apple-touch-icon links
// in index.html, the files the manifest names, and dist/sw.js with its precache list.
//
// For each page the script needs (a) a file dist/assets/<page>-<hash>.js and (b) a string that
// only that page renders, absent from the entry chunk. The strings are literals from the page's
// source; if a page is reworked so its string changes, update the table.
import { existsSync, readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const dist = join(fileURLToPath(new URL(".", import.meta.url)), "..", "dist");

const PAGES = {
  vms: "Proxmox activity",
  cluster: "Warning events",
  gitops: "Flux objects",
  hardware: "Disk bays",
  alerts: "firing since",
  status: "Average duration of the shown checks",
  "settings-page": "Settings are read-only",
  "plugin-settings-page": "No plugin named",
  "audit-page": "No loaded records of this kind",
  "tokens-page": "Type a user name, then Show.",
  "settings-nav": "Personal access tokens for scripts and agents.",
  "consent-page": "This request expired; start again from the app.",
  "connected-apps-page": "Apps with access to dash",
};

const html = readFileSync(join(dist, "index.html"), "utf8");
const entryFile = /<script[^>]*type="module"[^>]*src="\/?([^"]+\.js)"/.exec(html)?.[1];
if (!entryFile) {
  console.error("check-chunks: no module script in dist/index.html (run `npm run build` first)");
  process.exit(1);
}
const entry = readFileSync(join(dist, entryFile), "utf8");
const assets = readdirSync(join(dist, "assets"));

const problems = [];

// CSP readiness. Fonts must be files (build.assetsInlineLimit: 0); an inlined one is a `data:` URL.
const cssFiles = assets.filter((f) => f.endsWith(".css"));
if (cssFiles.length === 0) {
  problems.push("no .css file in dist/assets");
}
for (const f of cssFiles) {
  if (readFileSync(join(dist, "assets", f), "utf8").includes("url(data:")) {
    problems.push(`assets/${f} contains url(data:...): an inlined asset needs a data: CSP source`);
  }
}
// sonner's own stylesheet is a CSS file (imported statically); its stylesheet text inside a JS
// chunk means the injection of a runtime <style> is back, e.g. because a release moved the entry
// the vite-plugins/static-sonner.ts transform matches. The marker is the first rule of that CSS.
for (const f of assets.filter((a) => a.endsWith(".js"))) {
  if (readFileSync(join(dist, "assets", f), "utf8").includes("[data-sonner-toaster][dir=ltr]")) {
    problems.push(
      `assets/${f} contains sonner's stylesheet text: sonner injects a <style> at runtime again (see vite-plugins/static-sonner.ts)`,
    );
  }
}
if (/<script(?![^>]*\bsrc=)[^>]*>/i.test(html)) {
  problems.push("dist/index.html has an inline <script> (a script tag without src)");
}
if (/<style[\s>]/i.test(html)) {
  problems.push("dist/index.html has a <style> element");
}
const nonceMeta = /<meta\b[^>]*\bname="csp-nonce"[^>]*>/i.exec(html)?.[0];
if (!nonceMeta) {
  problems.push(
    'dist/index.html has no <meta name="csp-nonce"> (the server templates its content)',
  );
} else if (!/\bcontent="__CSP_NONCE__"/.test(nonceMeta)) {
  problems.push('the csp-nonce meta in dist/index.html must have content="__CSP_NONCE__"');
}
// The PWA (P7.6): the manifest link keeps crossorigin="use-credentials" (an identity proxy answers
// a fetch without its cookie with a redirect to its login), the apple-touch-icon link survives, and
// the files they and the manifest name are in dist.
const manifestLink = /<link\b[^>]*\brel="manifest"[^>]*>/i.exec(html)?.[0];
if (!manifestLink) {
  problems.push('dist/index.html has no <link rel="manifest">');
} else if (
  !/\bhref="\/manifest\.webmanifest"/.test(manifestLink) ||
  !/\bcrossorigin="use-credentials"/.test(manifestLink)
) {
  problems.push(
    'the manifest link in dist/index.html must be href="/manifest.webmanifest" crossorigin="use-credentials"',
  );
}
const touchIcon = /<link\b[^>]*\brel="apple-touch-icon"[^>]*\bhref="\/([^"]+)"/i.exec(html)?.[1];
if (!touchIcon) {
  problems.push('dist/index.html has no <link rel="apple-touch-icon" href="/…">');
}
const manifest = JSON.parse(readFileSync(join(dist, "manifest.webmanifest"), "utf8"));
for (const file of [
  "offline.html",
  ...(touchIcon ? [touchIcon] : []),
  ...manifest.icons.map((i) => i.src.replace(/^\//, "")),
]) {
  if (!existsSync(join(dist, file))) {
    problems.push(`dist/${file} is missing`);
  }
}
// The service worker (vite-plugins/service-worker.ts): dist/sw.js exists, and its precache list
// holds only /assets/*, /offline.html and the PNG icons (never index.html, whose nonce is per
// response, and never /api, /oauth or /mcp), every one a file in dist, with every asset in it.
if (!existsSync(join(dist, "sw.js"))) {
  problems.push("dist/sw.js is missing (vite-plugins/service-worker.ts emits it)");
} else {
  const sw = readFileSync(join(dist, "sw.js"), "utf8");
  const list = /^const PRECACHE = (\[.*\]);$/m.exec(sw)?.[1];
  const precache = list ? JSON.parse(list) : [];
  if (!list) {
    problems.push("dist/sw.js has no `const PRECACHE = [...]` line (an unfilled placeholder?)");
  }
  if (!/^const BUILD = "[0-9a-f]{16}";$/m.test(sw)) {
    problems.push("dist/sw.js has no build hash");
  }
  if (!precache.includes("/offline.html")) {
    problems.push("dist/sw.js does not precache /offline.html");
  }
  for (const url of precache) {
    if (!/^\/(?:assets\/[^/]+|offline\.html|icons\/[^/]+\.png)$/.test(url)) {
      problems.push(`dist/sw.js precaches ${url}: only /assets/*, /offline.html and icons may be`);
    } else if (!existsSync(join(dist, url))) {
      problems.push(`dist/sw.js precaches ${url}, which is not in dist`);
    }
  }
  for (const f of assets.filter((a) => !precache.includes(`/assets/${a}`))) {
    problems.push(`dist/assets/${f} is not in the precache list of dist/sw.js`);
  }
}
for (const [page, marker] of Object.entries(PAGES)) {
  const chunks = assets.filter((f) => f.startsWith(`${page}-`) && f.endsWith(".js"));
  if (chunks.length === 0) {
    problems.push(`no chunk named ${page}-*.js in dist/assets`);
    continue;
  }
  if (entry.includes(marker)) {
    problems.push(`${page}: "${marker}" is in the entry chunk ${entryFile}`);
  }
  const own = chunks.some((f) => readFileSync(join(dist, "assets", f), "utf8").includes(marker));
  if (!own) {
    problems.push(`${page}: "${marker}" is not in ${chunks.join(", ")}`);
  }
}

if (problems.length > 0) {
  console.error(`check-chunks: ${problems.length} problem(s)\n- ${problems.join("\n- ")}`);
  process.exit(1);
}
console.log(
  `check-chunks: ${Object.keys(PAGES).length} lazy page chunks, entry ${entryFile} clean`,
);
