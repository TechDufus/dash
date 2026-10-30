import { useSyncExternalStore } from "react";
import { TOPICS, type TopicData, type TypedEnvelope } from "./topics";
import type { Envelope, Topic } from "./types";

export type ConnectionState = "connecting" | "live" | "reconnecting";

const envelopes = new Map<Topic, Envelope>();
const listeners = new Set<() => void>();
let connection: ConnectionState = "connecting";
let source: EventSource | null = null;
let retryTimer: ReturnType<typeof setTimeout> | undefined;

function emit() {
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

function open() {
  source?.close();
  const es = new EventSource("/api/stream");
  source = es;
  es.onopen = () => setConnection("live");
  es.onerror = () => {
    setConnection(
      envelopes.size === 0 && connection === "connecting" ? "connecting" : "reconnecting",
    );
    // The browser retries by itself while CONNECTING; a CLOSED stream (HTTP error) needs a new one.
    if (es.readyState === EventSource.CLOSED) {
      clearTimeout(retryTimer);
      retryTimer = setTimeout(open, 3000);
    }
  };
  for (const topic of TOPICS) {
    es.addEventListener(topic, (ev) => {
      try {
        const env = JSON.parse((ev as MessageEvent<string>).data) as Envelope;
        envelopes.set(topic, env);
        setConnection("live");
        emit();
      } catch {
        // ignore a malformed event; the next one replaces it
      }
    });
  }
}

/** Opens the single shared EventSource (idempotent). */
export function startStream() {
  if (!source) open();
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
