import { handle401 } from "@/auth/handle401";
import type { APIError } from "./types";

export class ApiError extends Error {
  readonly status: number;
  /** The machine-readable reason (an ErrCode*); undefined for an answer that is not dash's. */
  readonly code?: string;
  /** Identifies the request in dash's logs and audit records. */
  readonly requestId?: string;
  /** The envelope's details: AuthDetails on a 401 unauthenticated from a session mode. */
  readonly details?: unknown;
  constructor(
    status: number,
    message: string,
    code?: string,
    requestId?: string,
    details?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.details = details;
  }
}

type Params = Record<string, string | number | undefined | null>;

export function withParams(path: string, params?: Params): string {
  if (!params) return path;
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== null && v !== "") q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `${path}?${s}` : path;
}

// handle401 (auth/handle401.ts) decides what a 401 means: a reload behind the identity proxy, the
// login or setup page in the local mode, the provider in the oidc mode; once per 30 s at most.
async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (method !== "GET" && method !== "HEAD") headers["X-Dash-Csrf"] = "1";
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let res: Response;
  try {
    res = await fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: "same-origin",
    });
  } catch (e) {
    throw new ApiError(0, e instanceof Error ? e.message : "network error");
  }
  if (!res.ok) {
    let message = res.statusText || `HTTP ${res.status}`;
    let code: string | undefined;
    let requestId = res.headers.get("X-Request-Id") ?? undefined;
    let details: unknown;
    try {
      const j = (await res.json()) as Partial<APIError>;
      if (j.error) message = j.error;
      code = j.code || undefined;
      requestId = j.requestId || requestId;
      details = j.details;
    } catch {
      // not JSON; keep the status text
    }
    const err = new ApiError(res.status, message, code, requestId, details);
    handle401(err);
    throw err;
  }
  if (res.status === 204) return undefined as T;
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export const api = {
  get: <T>(path: string, params?: Params) => request<T>("GET", withParams(path, params)),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body ?? {}),
  del: <T>(path: string) => request<T>("DELETE", path),
};

export function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}
