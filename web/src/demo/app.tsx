// The demo's app: what main.tsx renders, under a hash router and with the demo's banner. demo/main.ts
// imports it once the player is the transport. No service worker (a static host serves the demo
// beside other pages, and a recording needs no offline copy) and no public shell: the recorded
// user is signed in.
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { Router } from "wouter";
import { App } from "@/App";
import { queryClient } from "@/api/queries";
import { startStream } from "@/api/stream";
import { boot } from "@/boot";
import { installChunkReload } from "@/plugins/chunk-reload";
import { pluginsQuery } from "@/plugins/state";
import { DemoBanner } from "./banner";
import { useHashRoute } from "./router";
import "@/styles/globals.css";
import "sonner/dist/styles.css";

boot("/", {
  installChunkReload: () => {
    installChunkReload();
  },
  registerServiceWorker: () => {},
  startStream,
  prefetchPlugins: () => void queryClient.prefetchQuery(pluginsQuery),
});

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <StrictMode>
      <Router hook={useHashRoute}>
        <App />
        <DemoBanner />
      </Router>
    </StrictMode>,
  );
}
