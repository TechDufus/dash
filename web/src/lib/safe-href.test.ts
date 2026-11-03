import { describe, expect, it } from "vitest";
import { safeHref } from "./safe-href";

describe("safeHref", () => {
  it.each([
    "http://grafana.example.com",
    "https://grafana.example.com/d/abc?x=1#y",
    "HTTPS://GRAFANA.EXAMPLE.COM",
    "/apps/wiki",
    "wiki/page",
    "?q=1",
    "#top",
  ])("keeps %s unchanged", (url) => {
    expect(safeHref(url)).toBe(url);
  });

  it.each([
    "javascript:alert(1)",
    "JaVaScRiPt:alert(1)",
    "  javascript:alert(1)",
    "\tjavascript:alert(1)\n",
    "java\tscr\nipt:alert(1)",
    "\u0001javascript:alert(1)",
    "data:text/html,<script>alert(1)</script>",
    "DATA:text/html;base64,PHNjcmlwdD4=",
    "vbscript:msgbox(1)",
    "VbScript:msgbox(1)",
    "blob:https://example.com/0000",
    "file:///etc/passwd",
    "ftp://example.com/x",
    "mailto:a@example.com",
    "//[",
    "http://[",
    "https://exa mple.com",
  ])("refuses %j", (url) => {
    expect(safeHref(url)).toBeUndefined();
  });

  it("gives undefined for empty and missing input", () => {
    expect(safeHref("")).toBeUndefined();
    expect(safeHref(undefined)).toBeUndefined();
    expect(safeHref(null)).toBeUndefined();
    expect(safeHref(42 as unknown as string)).toBeUndefined();
  });
});
