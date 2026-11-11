// The static demo's entry (vite.demo.config.ts). It loads the recording's manifest, installs the
// recording's clock and the player as the transport, and only then imports the app: this module
// imports no app module itself, so every app module evaluates with the shim and the player in
// place. The recording sits beside index.html (recording/), wherever the demo is hosted.
import { setTransport } from "@/api/transport";
import { installClock, RecordedClock } from "./clock";
import { Player } from "./player";
import { checkManifest } from "./recording";

declare global {
  interface Window {
    /** What the smoke test reads: the GETs the recording had no answer for. */
    __dashDemo?: { misses: string[] };
  }
}

// The browser's fetch, before anything can replace it: the recording's own files go through it.
const browserFetch = globalThis.fetch.bind(globalThis);
const recording = new URL("recording/", document.baseURI);

async function load(file: string): Promise<string> {
  const res = await browserFetch(new URL(`data/${file}`, recording));
  if (!res.ok) throw new Error(`recording/data/${file}: HTTP ${res.status}`);
  return res.text();
}

async function start(): Promise<void> {
  const res = await browserFetch(new URL("manifest.json", recording));
  if (!res.ok) throw new Error(`recording/manifest.json: HTTP ${res.status}`);
  const manifest = checkManifest(await res.json());
  const clock = new RecordedClock(Date.parse(manifest.frames[0]?.at ?? ""));
  installClock(() => clock.now());
  const state = { misses: [] as string[] };
  window.__dashDemo = state;
  setTransport(
    new Player({
      manifest,
      load,
      clock,
      onMiss: (key) => {
        state.misses.push(key);
        console.warn(`dash demo: the recording has no answer for GET ${key}`);
      },
    }),
  );
  // Not a static import: the app's modules must evaluate after the clock and the transport are
  // installed, and the manifest those need arrives asynchronously.
  await import("./app");
}

start().catch((err: unknown) => {
  console.error("dash demo:", err);
  const root = document.getElementById("root");
  if (root) {
    root.textContent = "The demo's recording could not be loaded. Reload the page to try again.";
  }
});
