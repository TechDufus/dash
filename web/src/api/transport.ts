// The SPA's two network entry points: every request goes through transportFetch and every event
// stream through openEventSource. The app uses the browser's fetch and EventSource, looked up at
// call time so a test that stubs the globals (test/harness.tsx) reaches its stub. The static demo
// (src/demo) installs a player of recorded data with setTransport before any app module runs.

export interface Transport {
  fetch(input: string, init?: RequestInit): Promise<Response>;
  eventSource(url: string): EventSource;
}

const browser: Transport = {
  fetch: (input, init) => fetch(input, init),
  eventSource: (url) => new EventSource(url),
};

let current: Transport = browser;

/** Replaces the transport for the rest of the page's life (the demo entry). */
export function setTransport(next: Transport): void {
  current = next;
}

export function transportFetch(input: string, init?: RequestInit): Promise<Response> {
  return current.fetch(input, init);
}

export function openEventSource(url: string): EventSource {
  return current.eventSource(url);
}
