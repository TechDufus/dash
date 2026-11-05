// Must stay the first import: it sets window.__webpack_nonce__ before any other module's top level
// runs (see lib/nonce-setup.ts).
import "@/lib/nonce-setup";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "@/App";
import { queryClient } from "@/api/queries";
import { startStream } from "@/api/stream";
import { PublicApp } from "@/auth/public-app";
import { boot, registerServiceWorker } from "@/boot";
import { installChunkReload } from "@/plugins/chunk-reload";
import { pluginsQuery } from "@/plugins/state";
import "@/styles/globals.css";
// sonner's own stylesheet, bundled statically (vite-plugins/static-sonner.ts drops its <style> injection).
import "sonner/dist/styles.css";

// /login and /setup are the public shell; every other path is the app (boot.ts).
const shell = boot(window.location.pathname, {
  installChunkReload: () => {
    installChunkReload();
  },
  // Only a build has /sw.js; the dev server would answer it with the SPA.
  registerServiceWorker: () => {
    if (import.meta.env.PROD) registerServiceWorker();
  },
  startStream,
  prefetchPlugins: () => void queryClient.prefetchQuery(pluginsQuery),
});

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(<StrictMode>{shell === "public" ? <PublicApp /> : <App />}</StrictMode>);
}
