import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { PANELS } from "./panels";

/**
 * The generated plugin pages (docs/plugins/<id>.md, from cmd/dash/docs.go) list the panels each
 * module puts on another page. The list is kept beside the generator, so this holds it to the
 * registry: the same slots and panel ids, both ways.
 */
const dir = join(process.cwd(), "..", "docs", "plugins"); // vitest runs in web/
const pages = readdirSync(dir)
  .filter((f) => f.endsWith(".md") && f !== "README.md")
  .map((f) => f.slice(0, -".md".length));

function documented(id: string): string[] {
  const page = readFileSync(join(dir, `${id}.md`), "utf8");
  const start = page.indexOf("\n### Panels on other pages\n");
  if (start < 0) return [];
  const end = page.indexOf("\n#", start + 1);
  const section = page.slice(start, end < 0 ? undefined : end);
  return [...section.matchAll(/^\| .* \(`([^`]+)`\) \| .* \| `([^`]+)` \|$/gm)].map(
    (m) => `${m[1]}/${m[2]}`,
  );
}

describe("the plugin docs", () => {
  it("have a page for every module with panels", () => {
    expect(pages).toEqual(expect.arrayContaining(Object.keys(PANELS)));
  });

  it.each(pages)("%s lists exactly the panels the module contributes", (id) => {
    const want = Object.entries(PANELS[id] ?? {}).flatMap(([slot, panels]) =>
      (panels ?? []).map((p) => `${slot}/${p.id}`),
    );
    expect(documented(id).sort()).toEqual(want.sort());
  });
});
