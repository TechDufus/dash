import type { Plugin } from "vite";

// sonner 2.x ships its stylesheet as a JS string and injects it as a <style> element when its
// module is first evaluated:
//
//   function __insertCSS(code) { ...document.createElement('style')... }
//   __insertCSS("[data-sonner-toaster]{...}");
//
// A strict `style-src` (no 'unsafe-inline') blocks that element. The same CSS is published as
// `sonner/dist/styles.css`, which the app imports statically (src/main.tsx), so the stylesheet
// is bundled into the built CSS and loaded by <link>. This transform removes the injector and its
// call so sonner no longer writes a <style> of its own.
//
// It FAILS LOUDLY: if a sonner release changes the shape, the build breaks here instead of
// silently re-adding the <style> element that the CSP would then block at runtime.

const SONNER_ENTRY = /sonner\/dist\/index\.mjs(?:\?.*)?$/;

// The helper definition: from `function __insertCSS(` to the closing brace at column 0.
const DEFINITION = /^function __insertCSS\([^)]*\)\s*\{[\s\S]*?^\}[ \t]*\r?\n?/m;
// The call: a statement at column 0 whose argument is one string literal (any quote, escapes
// allowed), as emitted by sonner's bundler.
const CALL = /^__insertCSS\((["'`])(?:\\[\s\S]|(?!\1)[^\\])*\1\);?[ \t]*\r?\n?/m;

/** True for the id of sonner's ESM entry (query strings and Windows separators tolerated). */
export function isSonnerEntry(id: string): boolean {
  return SONNER_ENTRY.test(id.replaceAll("\\", "/"));
}

/**
 * Returns sonner's source without the `__insertCSS` definition and call. Throws when either is
 * absent, or when any reference to `__insertCSS` remains afterwards.
 */
export function removeSonnerStyleInjection(code: string): string {
  if (!DEFINITION.test(code)) {
    throw new Error(
      "static-sonner: sonner/dist/index.mjs no longer defines `function __insertCSS(...)`; " +
        "sonner changed how it injects its <style>. Update web/vite-plugins/static-sonner.ts " +
        "so the strict CSP (style-src without 'unsafe-inline') keeps working.",
    );
  }
  if (!CALL.test(code)) {
    throw new Error(
      'static-sonner: sonner/dist/index.mjs no longer calls `__insertCSS("...")` at the top ' +
        "level of the module; sonner changed how it injects its <style>. Update " +
        "web/vite-plugins/static-sonner.ts so the strict CSP keeps working.",
    );
  }
  const out = code.replace(CALL, "").replace(DEFINITION, "");
  if (out.includes("__insertCSS")) {
    throw new Error(
      "static-sonner: `__insertCSS` is still referenced in sonner/dist/index.mjs after the " +
        "transform (more than one call?); update web/vite-plugins/static-sonner.ts.",
    );
  }
  return out;
}

export function staticSonner(): Plugin {
  return {
    name: "dash:static-sonner",
    enforce: "pre",
    transform(code, id) {
      if (!isSonnerEntry(id)) return null;
      return { code: removeSonnerStyleInjection(code), map: null };
    },
  };
}
