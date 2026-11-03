/**
 * The URL itself when it is safe to put in an anchor or hand to `window.open`: an absolute or
 * relative http(s) URL. Everything else (`javascript:`, `data:`, `vbscript:`, `blob:`, anything that
 * does not parse) gives `undefined`, so the caller draws text instead of a link.
 *
 * The scheme is read the way the browser reads it: `new URL` strips leading/trailing whitespace and
 * embedded tabs and newlines and lowercases the scheme, so ` JaVa\tScRiPt:` is `javascript:` here
 * exactly as it would be in an `href`. A relative URL resolves against the page, so it is http(s)
 * whenever the page is.
 */
export function safeHref(url: string | null | undefined): string | undefined {
  if (typeof url !== "string" || url === "") return undefined;
  let parsed: URL;
  try {
    parsed = new URL(url, window.location.href);
  } catch {
    return undefined;
  }
  return parsed.protocol === "http:" || parsed.protocol === "https:" ? url : undefined;
}
