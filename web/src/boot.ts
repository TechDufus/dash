import { isPublicPath } from "@/auth/public-app";

/** What the app shell starts before its first render; parameters so a test can watch them. */
export interface BootDeps {
  installChunkReload(): void;
  registerServiceWorker(): void;
  startStream(): void;
  prefetchPlugins(): void;
}

/**
 * boot decides which shell `pathname` gets and starts what that shell needs. Both shells register
 * the service worker, so the offline page is there however dash was first opened. /login and
 * /setup (and below) are the public shell: no event stream and no plugin list, which would only
 * answer 401 before anyone has signed in. Every other path is the app, which opens the stream and
 * asks for the plugin list before the first render (the rail and the routes wait for it).
 */
export function boot(pathname: string, deps: BootDeps): "public" | "app" {
  deps.installChunkReload();
  deps.registerServiceWorker();
  if (isPublicPath(pathname)) return "public";
  deps.startStream();
  deps.prefetchPlugins();
  return "app";
}

/** The parts of window registerServiceWorker reads. */
export type ServiceWorkerHost = Pick<Window, "isSecureContext" | "navigator">;

/**
 * Registers /sw.js (the build emits it; see web/sw/sw.js) for the whole origin. A browser without
 * service workers, or a page that is not a secure context (plain http to anything but localhost),
 * registers nothing and returns false. A failed registration leaves the app as it is: it only
 * loses the offline page.
 */
export function registerServiceWorker(host: ServiceWorkerHost = window): boolean {
  if (!host.isSecureContext || !("serviceWorker" in host.navigator)) return false;
  host.navigator.serviceWorker.register("/sw.js", { scope: "/" }).catch((err: unknown) => {
    console.warn("dash: the service worker could not be registered", err);
  });
  return true;
}
