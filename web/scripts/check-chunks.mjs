// Run after `vite build`: every lazy page must be its own chunk, and the entry chunk must not
// contain the page's code (epic P2.7: "each module's page is its own chunk").
//
// For each page the script needs (a) a file dist/assets/<page>-<hash>.js and (b) a string that
// only that page renders, absent from the entry chunk. The strings are literals from the page's
// source; if a page is reworked so its string changes, update the table.
import { readdirSync, readFileSync } from "node:fs";
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
