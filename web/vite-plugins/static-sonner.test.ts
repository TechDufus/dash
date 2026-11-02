import { readFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { isSonnerEntry, removeSonnerStyleInjection, staticSonner } from "./static-sonner.ts";

// Shaped like sonner/dist/index.mjs: helper at the top, the call with a quoted CSS string (escaped
// quotes inside) further down, runtime code around both.
const FIXTURE = `'use client';
function __insertCSS(code) {
  if (!code || typeof document == 'undefined') return
  let head = document.head || document.getElementsByTagName('head')[0]
  let style = document.createElement('style')
  style.type = 'text/css'
  head.appendChild(style)
  ;style.styleSheet ? (style.styleSheet.cssText = code) : style.appendChild(document.createTextNode(code))
}

import React from 'react';

const toast = () => "kept";

__insertCSS("[data-sonner-toaster]{--x:1}[data-a=\\"b\\"]{color:red}")

export { toast };
`;

const REAL = join(import.meta.dirname, "..", "node_modules", "sonner", "dist", "index.mjs");

describe("removeSonnerStyleInjection", () => {
  it("removes the helper and the call and keeps the rest", () => {
    const out = removeSonnerStyleInjection(FIXTURE);
    expect(out).not.toContain("__insertCSS");
    expect(out).not.toContain("createElement('style')");
    expect(out).toContain("'use client';");
    expect(out).toContain("import React from 'react';");
    expect(out).toContain('const toast = () => "kept";');
    expect(out).toContain("export { toast };");
  });

  it("throws, naming the problem, when the helper definition is missing", () => {
    const noDefinition = FIXTURE.replace(/function __insertCSS[\s\S]*?\n}\n/, "");
    expect(() => removeSonnerStyleInjection(noDefinition)).toThrow(
      /static-sonner: .*no longer defines `function __insertCSS/,
    );
  });

  it("throws, naming the problem, when the call is missing", () => {
    const noCall = FIXTURE.replace(/^__insertCSS\(.*\)\n/m, "");
    expect(noCall).not.toBe(FIXTURE);
    expect(() => removeSonnerStyleInjection(noCall)).toThrow(
      /static-sonner: .*no longer calls `__insertCSS/,
    );
  });

  it("throws when a second call would survive the transform", () => {
    const twoCalls = `${FIXTURE}\n__insertCSS("a{}")\n`;
    expect(() => removeSonnerStyleInjection(twoCalls)).toThrow(/still referenced/);
  });

  it("transforms the real sonner/dist/index.mjs", () => {
    const real = readFileSync(REAL, "utf8");
    expect(real).toContain("__insertCSS(");

    const out = removeSonnerStyleInjection(real);
    expect(out).not.toBe(real);
    expect(out.length).toBeLessThan(real.length);
    expect(out).not.toContain("__insertCSS(");
    expect(out).not.toMatch(/createElement\(\s*["']style["']\s*\)/);
    // The runtime itself is intact.
    expect(out).toContain("export {");
    expect(out).toContain("Toaster");
  });
});

describe("staticSonner plugin", () => {
  const plugin = staticSonner();
  const transform = plugin.transform as (code: string, id: string) => unknown;

  it("matches only sonner's ESM entry", () => {
    expect(isSonnerEntry("/p/node_modules/sonner/dist/index.mjs")).toBe(true);
    expect(isSonnerEntry("/p/node_modules/sonner/dist/index.mjs?v=abc123")).toBe(true);
    expect(isSonnerEntry("C:\\p\\node_modules\\sonner\\dist\\index.mjs")).toBe(true);
    expect(isSonnerEntry("/p/node_modules/sonner/dist/index.js")).toBe(false);
    expect(isSonnerEntry("/p/node_modules/other/dist/index.mjs")).toBe(false);
  });

  it("leaves other modules untouched, even ones that mention __insertCSS", () => {
    expect(transform("__insertCSS('x')", "/p/node_modules/other/index.mjs")).toBeNull();
  });

  it("transforms sonner and fails the build when its shape changed", () => {
    const id = "/p/node_modules/sonner/dist/index.mjs";
    const result = transform(FIXTURE, id) as { code: string };
    expect(result.code).not.toContain("__insertCSS");
    expect(() => transform("export const x = 1;", id)).toThrow(/static-sonner/);
  });
});
