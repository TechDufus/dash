import type { APIError } from "./types";

export class ApiError extends Error {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
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

// An expired oauth2-proxy session answers 401 (or redirects away); reload once to sign in again.
function maybeReauth(status: number) {
  if (status !== 401) return;
  const key = "dash:reauth";
  const last = Number(sessionStorage.getItem(key) ?? 0);
  if (Date.now() - last < 30_000) return;
  sessionStorage.setItem(key, String(Date.now()));
  window.location.reload();
}

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
    maybeReauth(res.status);
    let message = res.statusText || `HTTP ${res.status}`;
    try {
      const j = (await res.json()) as Partial<APIError>;
      if (j.error) message = j.error;
    } catch {
      // not JSON; keep the status text
    }
    throw new ApiError(res.status, message);
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
