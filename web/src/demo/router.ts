// The demo's routing: the whole app route lives in the URL fragment, query included
// (`…/demo/#/cluster?tab=pods`), so a static host serves the one index.html for every route and
// a link to any page of the demo works. wouter's own useHashLocation keeps the query in the real
// search part instead (`?tab=pods#/cluster`), where it would survive a navigation to another page
// that does not set it; these hooks keep path and query together in the fragment.
import { useSyncExternalStore } from "react";
import type { BaseLocationHook, BaseSearchHook } from "wouter";

function subscribe(cb: () => void): () => void {
  addEventListener("hashchange", cb);
  return () => removeEventListener("hashchange", cb);
}

/** The fragment without its "#", as "/path?query"; "/" when there is none. */
function route(): string {
  const r = location.hash.replace(/^#\/?/, "/");
  return r === "" ? "/" : r;
}

const routePath = () => route().split("?")[0] || "/";
const routeSearch = () => route().split("?").slice(1).join("?");

/** Navigates to `to` ("/path?query"), pushing or replacing the history entry, as wouter does. */
export function navigateHash(to: string, opts: { replace?: boolean; state?: unknown } = {}): void {
  const oldURL = location.href;
  const url = new URL(oldURL);
  url.hash = to.startsWith("/") ? to : `/${to}`;
  const newURL = url.href;
  if (opts.replace) history.replaceState(opts.state ?? null, "", newURL);
  else history.pushState(opts.state ?? null, "", newURL);
  // pushState fires no hashchange; the subscribers above listen to it alone.
  dispatchEvent(new HashChangeEvent("hashchange", { oldURL, newURL }));
}

const useHashSearch: BaseSearchHook = () => useSyncExternalStore(subscribe, routeSearch);

/** The location hook of the demo's <Router>; its search hook and link format come with it. */
export const useHashRoute: BaseLocationHook = () => [
  useSyncExternalStore(subscribe, routePath),
  navigateHash,
];
useHashRoute.searchHook = useHashSearch;
// A <Link>'s href is the route as a fragment, so opening it in a new tab works too.
useHashRoute.hrefs = (href: string) => `#${href}`;
