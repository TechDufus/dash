// The requests of the sign-in pages and the user menu. They bypass api/client.ts on purpose: a 401
// from /login or /setup is an answer to show, not an expired session to reauthenticate, and the
// pages need the Retry-After header of a 429, which ApiError does not carry.
import { ApiError } from "@/api/client";
import type { APIError, AuthDetails, Me, SetupResult } from "@/api/types";
import { authDetails } from "./handle401";

/** The longest wait the pages report; a larger Retry-After is shown as this. */
export const MAX_RETRY_AFTER_S = 24 * 60 * 60;

/** Password bounds of POST /setup, in UTF-8 bytes (the server's own limits). */
export const MIN_PASSWORD_BYTES = 12;
export const MAX_PASSWORD_BYTES = 256;

export function passwordBytes(password: string): number {
  return new TextEncoder().encode(password).length;
}

/** An ApiError with the 429's Retry-After, in whole seconds (undefined when absent or not delta-seconds). */
export class AuthRequestError extends ApiError {
  readonly retryAfter?: number;
  constructor(
    status: number,
    message: string,
    code?: string,
    requestId?: string,
    details?: unknown,
    retryAfter?: number,
  ) {
    super(status, message, code, requestId, details);
    this.name = "AuthRequestError";
    this.retryAfter = retryAfter;
  }
}

export function parseRetryAfter(value: string | null): number | undefined {
  if (value === null) return undefined;
  const v = value.trim();
  if (!/^\d{1,10}$/.test(v)) return undefined;
  return Math.min(Number(v), MAX_RETRY_AFTER_S);
}

/** "5 seconds", "1 minute", "15 minutes": a wait rounded up to whole minutes from 60 s. */
export function formatWait(seconds: number): string {
  if (seconds < 60) return seconds === 1 ? "1 second" : `${seconds} seconds`;
  const m = Math.ceil(seconds / 60);
  return m === 1 ? "1 minute" : `${m} minutes`;
}

async function send(method: "GET" | "POST", path: string, body?: unknown): Promise<Response> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method === "POST") {
    headers["X-Dash-Csrf"] = "1";
    headers["Content-Type"] = "application/json";
  }
  try {
    return await fetch(path, {
      method,
      headers,
      body: method === "POST" ? JSON.stringify(body ?? {}) : undefined,
      credentials: "same-origin",
      cache: "no-store",
    });
  } catch {
    throw new AuthRequestError(0, "dash is unreachable");
  }
}

async function failure(res: Response): Promise<AuthRequestError> {
  let message = res.statusText || `HTTP ${res.status}`;
  let code: string | undefined;
  let requestId = res.headers.get("X-Request-Id") ?? undefined;
  let details: unknown;
  try {
    const j = (await res.json()) as Partial<APIError>;
    if (typeof j.error === "string" && j.error) message = j.error;
    code = j.code || undefined;
    requestId = j.requestId || requestId;
    details = j.details;
  } catch {
    // not JSON; keep the status text
  }
  return new AuthRequestError(
    res.status,
    message,
    code,
    requestId,
    details,
    res.status === 429 ? parseRetryAfter(res.headers.get("Retry-After")) : undefined,
  );
}

async function post(path: string, body: unknown): Promise<void> {
  const res = await send("POST", path, body);
  if (!res.ok) throw await failure(res);
}

/** POST /login: resolves on 204 (the session cookie is set); throws AuthRequestError otherwise. */
export function login(username: string, password: string): Promise<void> {
  return post("/login", { username, password });
}

/** POST /setup: resolves with the account it created (the user is signed in already). */
export async function setup(token: string, password: string): Promise<SetupResult> {
  const res = await send("POST", "/setup", { token, password });
  if (!res.ok) throw await failure(res);
  return (await res.json()) as SetupResult;
}

/**
 * The scopes of POST /auth/logout (D23): "this" ends this session; "user" every session of the
 * caller and their OAuth grants; "all" (an administrator) every session and every grant of every
 * user. Personal access tokens survive each.
 */
export type LogoutScope = "this" | "user" | "all";

/** POST /auth/logout with `scope`. */
export function logout(scope: LogoutScope): Promise<void> {
  return post("/auth/logout", { scope });
}

/** What GET /api/v1/me says about signing in: the user when signed in, the 401's details otherwise. */
export type Probe = { me: Me } | { details?: AuthDetails };

/** Reads /api/v1/me without the app's reauth path; any failure other than a 401 is an empty probe. */
export async function probe(): Promise<Probe> {
  let res: Response;
  try {
    res = await send("GET", "/api/v1/me");
  } catch {
    return {};
  }
  if (res.ok) {
    try {
      return { me: (await res.json()) as Me };
    } catch {
      return {};
    }
  }
  if (res.status !== 401) return {};
  const err = await failure(res);
  return err.code === "unauthenticated" ? { details: authDetails(err.details) } : {};
}

const TOKEN_PARAM = "token";
const MAX_TOKEN_LENGTH = 512;

/** The setup token of a URL fragment (#token=…), or null when it carries no usable one. */
export function setupTokenOf(hash: string): string | null {
  if (!hash.startsWith("#")) return null;
  const token = new URLSearchParams(hash.slice(1)).get(TOKEN_PARAM);
  if (!token || token.length > MAX_TOKEN_LENGTH) return null;
  return token;
}

/**
 * Removes the fragment from the address bar and the current history entry, so a setup token is
 * never shown, bookmarked or sent anywhere again. Idempotent; the path and query stay.
 */
export function stripFragment(): void {
  const { hash, pathname, search } = window.location;
  if (hash || window.location.href.endsWith("#")) {
    window.history.replaceState(window.history.state, "", pathname + search);
  }
}
