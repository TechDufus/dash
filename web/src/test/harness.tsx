// Shared test helpers: a fake EventSource, a fetch stub, a render helper and wire-shape builders.
// Tests install the fakes themselves; setup.ts removes them (vi.unstubAllGlobals) after each test.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { type RenderResult, render } from "@testing-library/react";
import { MotionConfig } from "motion/react";
import type { ReactElement } from "react";
import { vi } from "vitest";
import { Router } from "wouter";
import { memoryLocation } from "wouter/memory-location";
import type { Envelope, PluginInfo } from "@/api/types";
import { TooltipProvider } from "@/components/ui/tooltip";

/**
 * FakeEventSource stands in for the browser's EventSource. `installEventSource()` replaces the
 * global and clears `instances`; the code under test creates instances, the test drives them:
 * `open()`, `emit(name, data)` and `fail()`.
 */
export class FakeEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  /** Every instance created since the last installEventSource(), oldest first. */
  static instances: FakeEventSource[] = [];

  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  readonly url: string;
  readonly withCredentials = false;
  readyState: number = FakeEventSource.CONNECTING;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  /** Event names with at least one addEventListener call (never shrinks, like a listener leak check). */
  readonly listenedTo = new Set<string>();

  constructor(url: string | URL) {
    super();
    this.url = String(url);
    FakeEventSource.instances.push(this);
  }

  override addEventListener(
    type: string,
    listener: EventListenerOrEventListenerObject | null,
    options?: boolean | AddEventListenerOptions,
  ) {
    this.listenedTo.add(type);
    super.addEventListener(type, listener, options);
  }

  /** The connection is established: readyState OPEN, then `open` listeners run. */
  open() {
    this.readyState = FakeEventSource.OPEN;
    const ev = new Event("open");
    this.onopen?.(ev);
    this.dispatchEvent(ev);
  }

  /** The server sends a named event; an object `data` is JSON-encoded like dash's frames. */
  emit(name: string, data: unknown) {
    if (this.readyState === FakeEventSource.CLOSED) return;
    const raw = typeof data === "string" ? data : JSON.stringify(data);
    const ev = new MessageEvent(name, { data: raw });
    if (name === "message") this.onmessage?.(ev);
    this.dispatchEvent(ev);
  }

  /**
   * The connection broke. By default it is final (HTTP error or a redirect: readyState CLOSED, so
   * the page must open a new EventSource); `{ retrying: true }` is a network drop the browser
   * retries by itself (readyState CONNECTING).
   */
  fail({ retrying = false }: { retrying?: boolean } = {}) {
    this.readyState = retrying ? FakeEventSource.CONNECTING : FakeEventSource.CLOSED;
    const ev = new Event("error");
    this.onerror?.(ev);
    this.dispatchEvent(ev);
  }

  close() {
    this.readyState = FakeEventSource.CLOSED;
  }
}

/** Replaces the global EventSource with FakeEventSource and returns the class. */
export function installEventSource(): typeof FakeEventSource {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
  return FakeEventSource;
}

/** One request seen by stubFetch. `matched` is false when no route answered it (it got a 404). */
export interface FetchCall {
  method: string;
  path: string;
  query: URLSearchParams;
  headers: Headers;
  /** The JSON-decoded body; undefined when the request had none. */
  body: unknown;
  matched: boolean;
}

export type RouteHandler = (call: FetchCall) => unknown;

/**
 * A route answers `"METHOD /path"` (query strings are ignored when matching). The value is a
 * Response (sent as is), a function of the call returning any of these (or a promise of one), or
 * plain data (sent as 200 JSON). `undefined` is a 204.
 */
export type Routes = Record<string, unknown>;

export function json(body: unknown, status = 200, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", ...headers },
  });
}

export interface FetchStub {
  calls: FetchCall[];
  /** Calls that matched `"METHOD /path"`. */
  callsTo(key: string): FetchCall[];
  /** Adds or replaces a route after the stub was installed. */
  route(key: string, handler: unknown): void;
}

/** Replaces the global fetch with a stub that answers from `routes`; no network is involved. */
export function stubFetch(routes: Routes = {}): FetchStub {
  const table = new Map(Object.entries(routes));
  const calls: FetchCall[] = [];

  const stub = vi.fn(async (input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
    const url = new URL(
      typeof input === "string" || input instanceof URL ? String(input) : input.url,
      "http://dash.test",
    );
    const method = (init?.method ?? "GET").toUpperCase();
    const key = `${method} ${url.pathname}`;
    let body: unknown;
    if (typeof init?.body === "string") body = JSON.parse(init.body);
    const call: FetchCall = {
      method,
      path: url.pathname,
      query: url.searchParams,
      headers: new Headers(init?.headers),
      body,
      matched: table.has(key),
    };
    calls.push(call);
    if (!call.matched) return json({ error: `stubFetch: no route for ${key}` }, 404);

    const route = table.get(key);
    const result = typeof route === "function" ? await (route as RouteHandler)(call) : route;
    if (result instanceof Response) return result;
    if (result === undefined) return new Response(null, { status: 204 });
    return json(result);
  });
  vi.stubGlobal("fetch", stub);

  return {
    calls,
    callsTo: (key) => calls.filter((c) => `${c.method} ${c.path}` === key),
    route: (key, handler) => {
      table.set(key, handler);
    },
  };
}

export interface RenderAppOptions {
  /** The initial location (default "/"). */
  path?: string;
  queryClient?: QueryClient;
  /** Renders inside StrictMode, outermost, as main.tsx does (the providers inside it). */
  strict?: boolean;
}

export interface RenderAppResult extends RenderResult {
  queryClient: QueryClient;
  /** Navigate the memory location like a link click would. */
  navigate: (to: string) => void;
  /** Every location visited, starting with the initial path. */
  history: string[];
}

/**
 * Renders `ui` inside what the real app provides: a QueryClient without retries, a wouter memory
 * location, tooltips, and Motion with animations off (so transitions never hide an element).
 */
export function renderApp(
  ui: ReactElement,
  { path = "/", queryClient, strict = false }: RenderAppOptions = {},
) {
  const client =
    queryClient ??
    new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } } });
  const location = memoryLocation({ path, record: true });
  const result = render(ui, {
    reactStrictMode: strict,
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>
        <MotionConfig reducedMotion="always">
          <TooltipProvider delayDuration={0}>
            <Router hook={location.hook} searchHook={location.searchHook}>
              {children}
            </Router>
          </TooltipProvider>
        </MotionConfig>
      </QueryClientProvider>
    ),
  });
  return Object.assign(result, {
    queryClient: client,
    navigate: location.navigate,
    history: location.history,
  }) as RenderAppResult;
}

// Wire-shape builders. A fixed timestamp keeps snapshots and ordering deterministic.
const AT = "2026-01-01T00:00:00Z";

export function envelope<T>(topic: string, data: T, sources: Envelope["sources"] = []): Envelope {
  return { topic, updatedAt: AT, sources, data };
}

export function pluginInfo(partial: Partial<PluginInfo> = {}): PluginInfo {
  const id = partial.id ?? "demo";
  return {
    id,
    type: id,
    name: id,
    version: "test",
    state: "healthy",
    message: "",
    topics: [],
    generic: [],
    actions: [],
    nav: [],
    configurable: false,
    objects: false,
    ...partial,
  };
}

/** The `core.plugins` envelope for `list`, as the stream sends it. */
export function pluginsEnvelope(list: PluginInfo[]): Envelope {
  return envelope("core.plugins", { plugins: list });
}
