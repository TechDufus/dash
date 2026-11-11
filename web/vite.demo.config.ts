import { fileURLToPath, URL } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";
import { staticSonner } from "./vite-plugins/static-sonner.ts";

// The static demo (`npm run build:demo` → dist-demo): the real SPA with src/demo/main.ts as its
// entry, which replays a recording instead of talking to dash (docs/development.md, "Static
// demo"). A relative base, so it works under any path of any static host; no service worker, no
// web manifest and no public/ files, since the demo is a page of a site, not an installable app.

/** Each edit of index.html the demo needs: what it finds, and what replaces it. */
const HTML_EDITS: [RegExp, string][] = [
  [
    /<script type="module" src="\/src\/main\.tsx"><\/script>/,
    '<script type="module" src="/src/demo/main.ts"></script>',
  ],
  // The server templates the nonce; a static host serves no CSP.
  [/\s*<meta name="csp-nonce"[^>]*>/, ""],
  [/\s*<!--[^>]*manifest[^>]*-->\s*<link rel="manifest"[^>]*>/, ""],
  [/\s*<link rel="apple-touch-icon"[^>]*>/, ""],
  [/<title>dash<\/title>/, "<title>dash: recorded demo</title>"],
];

/**
 * index.html with the demo's entry and without what only the served app uses. It fails the build
 * when index.html changed so an edit no longer applies, rather than ship the app's entry.
 */
function demoHTML(): Plugin {
  return {
    name: "dash:demo-html",
    transformIndexHtml: {
      order: "pre",
      handler(html) {
        let out = html;
        for (const [find, replace] of HTML_EDITS) {
          if (!find.test(out)) {
            throw new Error(
              `demo-html: index.html no longer matches ${find}; update vite.demo.config.ts`,
            );
          }
          out = out.replace(find, replace);
        }
        return out;
      },
    },
  };
}

export default defineConfig({
  base: "./",
  publicDir: false,
  plugins: [demoHTML(), staticSonner(), react(), tailwindcss()],
  resolve: {
    alias: { "@": fileURLToPath(new URL("./src", import.meta.url)) },
  },
  build: { outDir: "dist-demo", emptyOutDir: true, assetsInlineLimit: 0 },
});
