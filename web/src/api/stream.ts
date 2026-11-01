import { useSyncExternalStore } from "react";
import { normalizePlugins } from "@/plugins/normalize";
import { isVisible } from "@/plugins/visibility";
import { api } from "./client";
import type { TopicData, TypedEnvelope } from "./topics";
import type { Envelope, PluginsData, Topic } from "./types";

export type ConnectionState = "connecting" | "live" | "reconnecting";

// The stream starts with these two. Every other topic is named by a plugin row in core.plugins,
// so a plugin this build has never heard of still gets its topics delivered (generic views).
const BOOT = ["core.plugins", "core.overview"] as const;

const envelopes = new Map<string, Envelope>();
const listeners = new Set<() => void>();
/** Topic names with an event listener on the current EventSource. */
const listening = new Set<string>();
/** Topics a visible plugin listed in the previous core.plugins event of this connection. */
let visibleTopics = new Set<string>();
/** True until the first core.plugins event of the connection: nothing is seeded on connect. */
let firstPlugins = true;
let version = 0;
let connection: ConnectionState = "connecting";
let source: EventSource | null = null;
let retryTimer: ReturnType<typeof setTimeout> | undefined;

function emit() {
  version++;
  for (const l of listeners) l();
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => {
    listeners.delete(l);
  };
}

function setConnection(next: ConnectionState) {
  if (connection === next) return;
  connection = next;
  emit();
}

function parse(ev: Event): Envelope | undefined {
  try {
    const env = JSON.parse((ev as MessageEvent<string>).data) as Envelope;
    return env && typeof env === "object" ? env : undefined;
  } catch {
    return undefined; // a malformed event is ignored; the next one replaces it
  }
}

/** Attaches the topic's listener unless it has one; true when it was attached now. */
function listen(es: EventSource, topic: string): boolean {
  if (listening.has(topic)) return false;
  listening.add(topic);
  es.addEventListener(topic, (ev) => {
    const env = parse(ev);
    if (!env) return;
    if (topic === "core.plugins") {
      const plugins = (env.data as PluginsData | undefined)?.plugins;
      if (!Array.isArray(plugins)) return;
      // Rows enter the store complete: a v0.5.0 server sends none with `generic`.
      const rows = normalizePlugins(plugins);
      envelopes.set(topic, { ...env, data: { ...(env.data as PluginsData), plugins: rows } });
      setConnection("live");
      syncTopics(es, rows);
      emit();
      return;
    }
    envelopes.set(topic, env);
    setConnection("live");
    emit();
  });
  return true;
}

/**
 * core.plugins decides what the stream listens to and what the store keeps: it attaches a
 * listener for every topic any plugin lists (an EventSource accepts listeners at any time and
 * delivers in order, so the listener exists before the next snapshot event), drops the envelope
 * of every non-core topic no visible plugin lists, and fetches the topics whose listener it
 * attached just now.
 */
function syncTopics(es: EventSource, plugins: PluginsData["plugins"]) {
  const next = new Set<string>();
  const attached = new Set<string>();
  for (const p of plugins) {
    for (const t of p.topics) {
      if (listen(es, t)) attached.add(t);
      if (isVisible(p.state)) next.add(t);
    }
  }
  for (const name of [...envelopes.keys()]) {
    if (!name.startsWith("core.") && !next.has(name)) envelopes.delete(name);
  }
  const fresh = [...next].filter((t) => attached.has(t) && !envelopes.has(t));
  const seed = !firstPlugins;
  visibleTopics = next;
  firstPlugins = false;
  // On connect the snapshot follows core.plugins on the same stream. A plugin this page has not
  // heard of can have published before its listener existed, and the hub only publishes changes,
  // so its first value is fetched once. A topic that was already listened to needs no fetch: its
  // publish reaches the listener whether the plugin row turned visible before it or after, and a
  // fetch would race that publish (404 until the plugin has published).
  if (seed) for (const t of fresh) void seedTopic(es, t);
}

async function seedTopic(es: EventSource, name: string) {
  try {
    const env = await api.get<Envelope>(`/api/v1/topics/${name}`);
    if (source !== es || envelopes.has(name) || !visibleTopics.has(name) || !env) return;
    envelopes.set(name, env);
    emit();
  } catch {
    // 404: the plugin has not published yet; the stream delivers it when it does
  }
}

function open() {
  source?.close();
  listening.clear();
  visibleTopics = new Set();
  firstPlugins = true;
  const es = new EventSource("/api/v1/stream");
  source = es;
  es.onopen = () => setConnection("live");
  es.onerror = () => {
    setConnection(
      envelopes.size === 0 && connection === "connecting" ? "connecting" : "reconnecting",
    );
    // The browser retries by itself while CONNECTING; a CLOSED stream (HTTP error, or oauth2-proxy
    // redirecting to GitHub after the session expired) needs a new one. The /api/v1/me probe runs the
    // 401 -> reload reauth path in the client when the session is gone.
    if (es.readyState === EventSource.CLOSED) {
      void api.get("/api/v1/me").catch(() => {});
      clearTimeout(retryTimer);
      retryTimer = setTimeout(open, 3000);
    }
  };
  for (const topic of BOOT) listen(es, topic);
}

/** Opens the single shared EventSource (idempotent). */
export function startStream() {
  if (!source) open();
}

/** Closes the stream and forgets everything it delivered (tests; the app never calls it). */
export function stopStream() {
  clearTimeout(retryTimer);
  source?.close();
  source = null;
  listening.clear();
  visibleTopics = new Set();
  firstPlugins = true;
  envelopes.clear();
  connection = "connecting";
  emit();
}

/** The current envelope of a topic, by name; for code that is not a component (the palette). */
export function getEnvelope(name: string): Envelope | undefined {
  return envelopes.get(name);
}

/** A counter that changes with every stream change; lets a component re-read `getEnvelope`. */
export function useStreamVersion(): number {
  return useSyncExternalStore(subscribe, () => version);
}

/** The envelope of a topic the SPA has no static type for (generic plugins). */
export function useEnvelope(name: string): Envelope | undefined {
  return useSyncExternalStore(subscribe, () => envelopes.get(name));
}

export function useTopic<T extends Topic>(topic: T): TypedEnvelope<T> | undefined {
  return useSyncExternalStore(
    subscribe,
    () => envelopes.get(topic) as TypedEnvelope<T> | undefined,
  );
}

/** Convenience: just the data payload of a topic. */
export function useTopicData<T extends Topic>(topic: T): TopicData[T] | undefined {
  return useTopic(topic)?.data;
}

export function useConnection(): ConnectionState {
  return useSyncExternalStore(subscribe, () => connection);
}
