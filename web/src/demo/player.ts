// The static demo's transport (api/transport.ts): it answers the SPA from a recording instead of a
// dash. A GET answers from the recorded response, the event stream replays the recorded frames of
// every topic in a loop, and any write answers one fixed error that says this is a recorded,
// read-only demo, through the API's own error envelope so every error surface shows it as is.
import type { Transport } from "@/api/transport";
import type { APIError } from "@/api/types";
import type { RecordedClock } from "./clock";
import { type Frame, type Manifest, recordingKey } from "./recording";

export const READ_ONLY_CODE = "demo_read_only";
export const READ_ONLY_MESSAGE =
  "This is a recorded, read-only demo of dash: nothing here can change. To run the real thing, " +
  "with demo data: docker run -p 8080:8080 -e DASH_DEMO=true -e DASH_SERVER_LISTEN=0.0.0.0:8080 " +
  "ghcr.io/techdufus/dash";

/** The one stream URL the recording replays; every other EventSource fails as a closed stream. */
const STREAM_PATH = "/api/v1/stream";
const TOPIC_PREFIX = "/api/v1/topics/";
const CORE_PLUGINS = "core.plugins";
/** The request id of the player's own answers: no dash logged them. */
const REQUEST_ID = "recorded-demo";

export interface PlayerOptions {
  manifest: Manifest;
  /** The text of a file of the recording's data/ (cached by the player). */
  load(file: string): Promise<string>;
  clock: RecordedClock;
  /** Records the key of a GET the recording has no answer for (the smoke test reads them). */
  onMiss(key: string): void;
}

/** An answer in the API's error envelope. */
function errorResponse(status: number, code: string, error: string): Response {
  const body: APIError = { error, code, requestId: REQUEST_ID };
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json", "X-Request-Id": REQUEST_ID },
  });
}

/** The topics of a frame in the order the stream sends them: core.plugins first, as dash does. */
function topicOrder(frame: Frame): string[] {
  const names = Object.keys(frame.topics);
  return names.includes(CORE_PLUGINS)
    ? [CORE_PLUGINS, ...names.filter((n) => n !== CORE_PLUGINS)]
    : names;
}

/** The player of one recording: the transport the demo installs, and the frame loop behind it. */
export class Player implements Transport {
  private readonly files = new Map<string, Promise<string>>();
  private readonly streams = new Set<ReplaySource>();
  private readonly frames: Frame[];
  private readonly stepMs: number;
  /** The frame on screen, and the text of each of its topics once loaded. */
  private index = 0;
  private shown: Map<string, string> | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;

  constructor(private readonly opts: PlayerOptions) {
    this.frames = opts.manifest.frames;
    this.stepMs = opts.manifest.stepSeconds * 1000;
    opts.clock.show(Date.parse(this.frames[0]?.at ?? ""));
  }

  /** A file of the recording, fetched once. */
  file(name: string): Promise<string> {
    let text = this.files.get(name);
    if (!text) {
      text = this.opts.load(name);
      // A failed load is forgotten, so the next use tries again.
      text.catch(() => this.files.delete(name));
      this.files.set(name, text);
    }
    return text;
  }

  fetch = async (input: string, init?: RequestInit): Promise<Response> => {
    const method = (init?.method ?? "GET").toUpperCase();
    if (method !== "GET" && method !== "HEAD") {
      return errorResponse(405, READ_ONLY_CODE, READ_ONLY_MESSAGE);
    }
    const url = new URL(input, "http://recording.invalid");
    const key = recordingKey(url.pathname, url.searchParams);
    // A topic the stream seeds by GET answers from the frame on screen, as dash would.
    const file =
      this.opts.manifest.rest[key] ??
      (url.pathname.startsWith(TOPIC_PREFIX) && url.search === ""
        ? this.frames[this.index]?.topics[url.pathname.slice(TOPIC_PREFIX.length)]
        : undefined);
    if (!file) {
      this.opts.onMiss(key);
      return errorResponse(404, "not_found", "not found");
    }
    const body = await this.file(file);
    return new Response(method === "HEAD" ? null : body, {
      status: 200,
      headers: {
        "Content-Type": file.endsWith(".json") ? "application/json" : "text/plain; charset=utf-8",
      },
    });
  };

  eventSource = (url: string): EventSource => {
    const source = new ReplaySource(url, this);
    if (new URL(url, "http://recording.invalid").pathname === STREAM_PATH) {
      void this.attach(source);
    } else {
      // The log tail and any other stream: what a stream the server refused looks like.
      setTimeout(() => source.fail(), 0);
    }
    return source as unknown as EventSource;
  };

  /** The text of every topic of frame i, in the order the stream sends them. */
  private async loadFrame(i: number): Promise<Map<string, string>> {
    const frame = this.frames[i];
    if (!frame) return new Map();
    const names = topicOrder(frame);
    const texts = await Promise.all(names.map((t) => this.file(frame.topics[t] as string)));
    return new Map(names.map((t, k) => [t, texts[k] as string]));
  }

  /** Opens source on the frame on screen and starts the loop with the first stream. */
  private async attach(source: ReplaySource): Promise<void> {
    if (!this.shown) {
      const i = this.index;
      let texts: Map<string, string>;
      try {
        texts = await this.loadFrame(i);
      } catch {
        source.fail();
        return;
      }
      // The loop may have moved on while this loaded; then its frame is the one on screen.
      if (this.index === i && !this.shown) this.shown = texts;
    }
    if (source.readyState === ReplaySource.CLOSED) return;
    this.streams.add(source);
    source.open();
    this.deliver(source);
    if (this.timer === undefined) this.schedule();
  }

  detach(source: ReplaySource): void {
    this.streams.delete(source);
    if (this.streams.size === 0) {
      clearTimeout(this.timer);
      this.timer = undefined;
    }
  }

  private schedule(): void {
    const next = (this.index + 1) % this.frames.length;
    // Load the next frame ahead, so it is on hand when its step comes.
    const texts = this.loadFrame(next);
    texts.catch(() => {});
    const timer = setTimeout(() => void this.advance(next, texts, timer), this.stepMs);
    this.timer = timer;
  }

  /** Shows frame `next`: the clock moves to its instant and every stream gets what changed. */
  private async advance(
    next: number,
    pending: Promise<Map<string, string>>,
    timer: ReturnType<typeof setTimeout>,
  ): Promise<void> {
    try {
      const texts = await pending;
      this.index = next;
      this.shown = texts;
      const frame = this.frames[next];
      if (frame) this.opts.clock.show(Date.parse(frame.at));
      for (const s of this.streams) this.deliver(s);
    } catch {
      // A frame that did not load stays the next one; schedule loads it again.
    }
    // Unless the last stream closed meanwhile (and maybe a new one started its own loop).
    if (this.timer === timer) this.schedule();
  }

  /** Sends source every topic of the frame on screen whose file differs from what it last got. */
  private deliver(source: ReplaySource): void {
    const frame = this.frames[this.index];
    if (!frame || !this.shown) return;
    for (const topic of topicOrder(frame)) {
      const file = frame.topics[topic];
      const text = this.shown.get(topic);
      if (!file || text === undefined || source.sent.get(topic) === file) continue;
      source.sent.set(topic, file);
      source.emit(topic, text);
    }
  }
}

/** The part of EventSource the SPA uses, fed by the player. */
export class ReplaySource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSED = 2;
  readonly withCredentials = false;
  readyState: number = ReplaySource.CONNECTING;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  /** The file of each topic this stream last sent. */
  readonly sent = new Map<string, string>();

  constructor(
    readonly url: string,
    private readonly player: Player,
  ) {
    super();
  }

  open(): void {
    this.readyState = ReplaySource.OPEN;
    const ev = new Event("open");
    this.onopen?.(ev);
    this.dispatchEvent(ev);
  }

  emit(name: string, data: string): void {
    if (this.readyState !== ReplaySource.OPEN) return;
    const ev = new MessageEvent(name, { data });
    if (name === "message") this.onmessage?.(ev);
    this.dispatchEvent(ev);
  }

  /** The stream ended for good, as a browser reports an HTTP error: readyState CLOSED, then error. */
  fail(): void {
    if (this.readyState === ReplaySource.CLOSED) return;
    this.readyState = ReplaySource.CLOSED;
    this.player.detach(this);
    const ev = new Event("error");
    this.onerror?.(ev);
    this.dispatchEvent(ev);
  }

  close(): void {
    this.readyState = ReplaySource.CLOSED;
    this.player.detach(this);
  }
}
