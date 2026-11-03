// The mode-aware answer to a 401 (design D13). A pure module: no React, no fetch. It decides from
// the error's details how the user signs in again and performs at most one navigation per 30 s,
// through the sessionStorage key the client's reauth guard has always used, so neither a reload
// loop nor a redirect loop can start (and none at all from the public pages).
import type { AuthDetails } from "@/api/types";

/** The navigations handle401 may perform; window.location in the browser. */
export interface Nav {
  reload(): void;
  assign(url: string): void;
}

/** The sessionStorage key of the 30 s guard, shared by every reauth path. */
export const REAUTH_KEY = "dash:reauth";
export const REAUTH_WINDOW_MS = 30_000;

/** The longest `next` the SPA sends or follows; anything longer falls back to "/". */
export const MAX_NEXT_LENGTH = 2048;

/** window.location as a Nav. A plain object, so tests can spy on its methods. */
export const browserNav: Nav = {
  reload: () => window.location.reload(),
  assign: (url) => window.location.assign(url),
};

const PUBLIC_PREFIXES = ["/login", "/setup"];

/** The public shell's paths: /login, /setup and anything below them. */
export function isPublicPath(pathname: string): boolean {
  return PUBLIC_PREFIXES.some((p) => pathname === p || pathname.startsWith(`${p}/`));
}

function hasControl(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x20 || (c >= 0x7f && c <= 0x9f)) return true;
  }
  return false;
}

/**
 * validNext returns the same-origin path to navigate to after signing in, or undefined when `raw`
 * is not one: it must start with a single "/", contain no backslash and no control character, stay
 * on `origin` once parsed, and not lead back to a public page (that would be a loop). The result
 * is the parsed path, query and fragment, and it is validated again **after** that normalization:
 * dot segments can turn an innocent-looking input (`/.//evil.example/x`, `/%2e//…`, `/a/..//…`)
 * into `//evil.example/x`, which a browser follows as a protocol-relative URL. So the output must
 * not start with `//`, must hold no backslash or control character (also percent-encoded, in the
 * path), must parse back to itself on `origin`, and is what every caller navigates to.
 */
export function validNext(
  raw: string | null | undefined,
  origin: string = window.location.origin,
): string | undefined {
  if (typeof raw !== "string" || raw.length === 0 || raw.length > MAX_NEXT_LENGTH) return undefined;
  if (raw[0] !== "/" || raw[1] === "/") return undefined;
  if (raw.includes("\\") || hasControl(raw)) return undefined;
  let url: URL;
  try {
    url = new URL(raw, origin);
  } catch {
    return undefined;
  }
  if (url.origin !== origin || isPublicPath(url.pathname)) return undefined;
  const out = url.pathname + url.search + url.hash;
  if (out.length > MAX_NEXT_LENGTH || !safeOutput(out, url.pathname, origin)) return undefined;
  return out;
}

/**
 * safeOutput is the check of a normalized next: not protocol-relative, no backslash or control
 * character (in the path also after percent-decoding, since a server or a browser may decode
 * it), and a fixed point: parsed again on `origin` it stays on `origin` and gives itself back.
 */
function safeOutput(out: string, pathname: string, origin: string): boolean {
  if (out[0] !== "/" || out[1] === "/" || out.includes("\\") || hasControl(out)) return false;
  let decoded: string;
  try {
    decoded = decodeURIComponent(pathname);
  } catch {
    return false;
  }
  if (decoded.includes("\\") || hasControl(decoded)) return false;
  try {
    const again = new URL(out, origin);
    return again.origin === origin && again.pathname + again.search + again.hash === out;
  } catch {
    return false;
  }
}

/** The current page as a `next` value: its path and query when valid, "/" otherwise. */
export function safeNext(
  loc: { pathname: string; search: string },
  origin: string = window.location.origin,
): string {
  return validNext(loc.pathname + loc.search, origin) ?? "/";
}

/** The AuthDetails of a 401 envelope, or undefined when `details` is not one. */
export function authDetails(details: unknown): AuthDetails | undefined {
  if (typeof details !== "object" || details === null) return undefined;
  const d = details as Record<string, unknown>;
  if (typeof d.mode !== "string") return undefined;
  const oidc = d.oidc;
  const issuer =
    typeof oidc === "object" && oidc !== null
      ? (oidc as Record<string, unknown>).issuer
      : undefined;
  return {
    mode: d.mode,
    setup: d.setup === true,
    ...(typeof issuer === "string" ? { oidc: { issuer } } : {}),
  };
}

/**
 * Takes the 30 s guard: true when no reauth navigation happened in the last 30 s (and records
 * this one). Fails closed: when sessionStorage cannot be read, nothing is done, because without
 * the guard a reload could loop. A timestamp in the future (the clock went back) does not block.
 */
function takeGuard(): boolean {
  const now = Date.now();
  let last: number;
  try {
    last = Number(sessionStorage.getItem(REAUTH_KEY) ?? 0);
  } catch {
    return false;
  }
  if (Number.isFinite(last) && last <= now && now - last < REAUTH_WINDOW_MS) return false;
  try {
    sessionStorage.setItem(REAUTH_KEY, String(now));
  } catch {
    return false;
  }
  return true;
}

/**
 * handle401 sends the user to sign in again after a 401. It acts on a 401 whose code is
 * "unauthenticated", and on a 401 that is not dash's answer at all (no code: an identity proxy in
 * front of dash refused the request, the proxy-mode path of v0.11); a 401 with any other code
 * (invalid_token, …) is the caller's to show. By `details.mode`: proxy, an unknown mode or no
 * details reload the page (the proxy signs the user in); local goes to /login?next=… or to /setup
 * while the account does not exist; oidc goes to /auth/oidc/start?next=…. Never from a public
 * page, and at most once per 30 s.
 */
export function handle401(
  err: { status: number; code?: string; details?: unknown },
  nav: Nav = browserNav,
  loc: { pathname: string; search: string; hash: string } = window.location,
): void {
  if (err.status !== 401) return;
  if (err.code !== undefined && err.code !== "unauthenticated") return;
  if (isPublicPath(loc.pathname)) return;
  const details = err.code === undefined ? undefined : authDetails(err.details);
  if (!takeGuard()) return;
  const next = encodeURIComponent(safeNext(loc));
  switch (details?.mode) {
    case "local":
      nav.assign(details.setup ? "/setup" : `/login?next=${next}`);
      return;
    case "oidc":
      nav.assign(`/auth/oidc/start?next=${next}`);
      return;
    default:
      nav.reload();
  }
}
