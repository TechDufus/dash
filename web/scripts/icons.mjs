// The PWA icons (web/public/icons), rendered from the favicon glyph in index.html with
// Playwright's pinned Chromium, so the home-screen icon and the tab icon stay one design:
//
//   npm run icons        (from web/; or `make icons` from the repository root)
//
// The glyph is the `data:` SVG of index.html's <link rel="icon">, drawn on the app background
// (#04060b, globals.css). Each icon is one screenshot of a fixed-size element at device scale 1:
//
//   icon-192.png, icon-512.png   purpose "any": a rounded square, transparent outside it
//   maskable-512.png             purpose "maskable": full bleed, the glyph inside the safe zone
//                                (the centre circle of radius 40%)
//   apple-touch-icon-180.png     full bleed: iOS rounds the corners and shows no transparency
//
// The PNG text and time chunks are dropped, so a rerun on one machine writes the same bytes; the
// PNGs are committed and the build copies them from public/.
//
// Needs Playwright's pinned Chromium once: `cd web && npm ci --ignore-scripts && npx playwright
// install chromium` (without node_modules, npx would fetch the latest Playwright, not the pinned).
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { chromium } from "playwright";

const web = join(fileURLToPath(new URL(".", import.meta.url)), "..");
const outDir = join(web, "public", "icons");
const BACKGROUND = "#04060b";

// name, size, glyph size (the glyph's 32-unit box; its outer circle has radius 15 units), corner
// radius (0 = full bleed). Maskable: a 430 px box puts the outer circle at radius 201.6 px, inside
// the 204.8 px safe zone (and the gradient fades out before it).
const ICONS = [
  ["icon-192.png", 192, 168, 42],
  ["icon-512.png", 512, 448, 112],
  ["maskable-512.png", 512, 430, 0],
  ["apple-touch-icon-180.png", 180, 136, 0],
];

/** The favicon's `data:` URL from index.html; fails when the link is gone or not an SVG. */
function faviconURL() {
  const html = readFileSync(join(web, "index.html"), "utf8");
  const link = html.match(
    /<link\s+rel="icon"\s+type="image\/svg\+xml"\s+href="(data:image\/svg\+xml,[^"]+)"/,
  );
  if (!link) {
    throw new Error('index.html: no <link rel="icon" type="image/svg+xml" href="data:…">');
  }
  return link[1];
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

async function main() {
  const glyph = faviconURL();
  mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch({ args: ["--force-color-profile=srgb"] });
  try {
    const page = await browser.newPage({
      viewport: { width: 600, height: 600 },
      deviceScaleFactor: 1,
    });
    // No request leaves the page: the glyph is a data: URL.
    await page.route("**/*", (route) =>
      route.request().url().startsWith("data:") ? route.continue() : route.abort("blockedbyclient"),
    );
    for (const [name, size, box, radius] of ICONS) {
      await page.setContent(
        `<!doctype html><html><body style="margin:0;background:transparent">` +
          `<div id="icon" style="width:${size}px;height:${size}px;border-radius:${radius}px;` +
          `background:${BACKGROUND};display:flex;align-items:center;justify-content:center">` +
          `<img alt="" src="${glyph}" style="width:${box}px;height:${box}px;display:block"></div>` +
          `</body></html>`,
      );
      await page.locator("#icon img").evaluate((img) => img.decode());
      const png = await page.locator("#icon").screenshot({ omitBackground: true });
      writeFileSync(join(outDir, name), stripPNG(png));
      console.log(`icons: ${name} (${size}x${size})`);
    }
  } finally {
    await browser.close();
  }
}

main().catch((err) => {
  console.error(`icons: ${err.stack ?? err}`);
  process.exit(1);
});
