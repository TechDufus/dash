import { isPublicPath } from "@/auth/public-app";

/** What the app shell starts before its first render; parameters so a test can watch them. */
export interface BootDeps {
  installChunkReload(): void;
  startStream(): void;
  prefetchPlugins(): void;
}

/**
 * boot decides which shell `pathname` gets and starts what that shell needs. /login and /setup
 * (and below) are the public shell: no event stream and no plugin list, which would only answer
 * 401 before anyone has signed in. Every other path is the app, which opens the stream and asks
 * for the plugin list before the first render (the rail and the routes wait for it).
 */
export function boot(pathname: string, deps: BootDeps): "public" | "app" {
  deps.installChunkReload();
  if (isPublicPath(pathname)) return "public";
  deps.startStream();
  deps.prefetchPlugins();
  return "app";
}
