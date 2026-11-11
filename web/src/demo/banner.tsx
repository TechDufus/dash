import { X } from "lucide-react";
import { useState } from "react";

const DISMISSED = "dash:demo-banner-dismissed";
/** The landing page's deploy section: the demo is published at <site>/demo/. */
const DEPLOY_URL = "../#deploy";
const REPO_URL = "https://github.com/techdufus/dash";

/**
 * The static demo's notice: a recorded demo, read-only, with the ways to run the real one. It
 * floats at the bottom left, clear of the toasts at the bottom right, and stays dismissed for the
 * rest of the browser session.
 */
export function DemoBanner() {
  const [open, setOpen] = useState(() => sessionStorage.getItem(DISMISSED) === null);
  if (!open) return null;
  return (
    <aside
      aria-label="About this demo"
      className="fixed bottom-3 left-3 z-40 flex max-w-[calc(100vw-1.5rem)] items-center gap-3 rounded-full border border-line bg-canvas/80 py-1.5 pr-1.5 pl-4 text-xs text-muted shadow-lg backdrop-blur-xl md:left-[88px]"
    >
      <p>
        <span className="font-medium text-fg">Recorded demo</span>, read-only.{" "}
        <a className="text-accent underline-offset-4 hover:underline" href={DEPLOY_URL}>
          Run your own
        </a>{" "}
        ·{" "}
        <a
          className="text-accent underline-offset-4 hover:underline"
          href={REPO_URL}
          rel="noopener noreferrer"
        >
          GitHub
        </a>
      </p>
      <button
        type="button"
        aria-label="Dismiss"
        className="grid size-6 shrink-0 place-items-center rounded-full hover:bg-panel hover:text-fg"
        onClick={() => {
          sessionStorage.setItem(DISMISSED, "1");
          setOpen(false);
        }}
      >
        <X aria-hidden="true" className="size-3.5" />
      </button>
    </aside>
  );
}
