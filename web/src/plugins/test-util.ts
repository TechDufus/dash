// Test helpers for the P2.8 tests: a live stream (the real stream.ts over a FakeEventSource) and
// the plugin rows that stand for the shipped shims.
import { act } from "@testing-library/react";
import { startStream, stopStream } from "@/api/stream";
import type { PluginInfo } from "@/api/types";
import {
  envelope,
  type FakeEventSource,
  installEventSource,
  pluginsEnvelope,
} from "@/test/harness";

export interface TestStream {
  es: FakeEventSource;
  /** Sends the plugin list, as the server does on every change of any row. */
  plugins(list: PluginInfo[]): void;
  /** Sends a topic's envelope. */
  send(topic: string, data: unknown, sources?: Parameters<typeof envelope>[2]): void;
  /** Stops the stream and forgets what it delivered. */
  close(): void;
}

/** Starts the app's stream over a FakeEventSource and opens it. */
export function connectStream(): TestStream {
  const ES = installEventSource();
  startStream();
  const es = ES.instances[0];
  if (!es) throw new Error("the stream did not open an EventSource");
  act(() => es.open());
  return {
    es,
    plugins: (list) => act(() => es.emit("core.plugins", pluginsEnvelope(list))),
    send: (topic, data, sources) => act(() => es.emit(topic, envelope(topic, data, sources))),
    close: () => act(() => stopStream()),
  };
}
