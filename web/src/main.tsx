// Must stay the first import: it sets window.__webpack_nonce__ before any other module's top level
// runs (see lib/nonce-setup.ts).
import "@/lib/nonce-setup";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "@/App";
import { queryClient } from "@/api/queries";
import { startStream } from "@/api/stream";
import { isPublicPath, PublicApp } from "@/auth/public-app";
import { installChunkReload } from "@/plugins/chunk-reload";
import { pluginsQuery } from "@/plugins/state";
import "@/styles/globals.css";
// sonner's own stylesheet, bundled statically (vite-plugins/static-sonner.ts drops its <style> injection).
import "sonner/dist/styles.css";

// /login and /setup are the public shell: no event stream and no plugin list, which would only
// answer 401 before anyone has signed in.
const publicShell = isPublicPath(window.location.pathname);
installChunkReload();
if (!publicShell) {
  startStream();
  // The rail and the routes wait for the plugin list: ask for it before the first render, so the
  // first paint usually has it (the stream's core.plugins takes over once it arrives).
  void queryClient.prefetchQuery(pluginsQuery);
}

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(<StrictMode>{publicShell ? <PublicApp /> : <App />}</StrictMode>);
}
