// The recording the static demo replays (web/scripts/demo-export.mjs writes it): a manifest plus
// content-addressed files in data/, all beside the demo's index.html under recording/.

/** recording/manifest.json. */
export interface Manifest {
  version: 1;
  /** Seconds between two frames; the player shows one frame per step. */
  stepSeconds: number;
  /** The stream's topics at instants stepSeconds apart, oldest first. */
  frames: Frame[];
  /** Answers to GET requests: the recordingKey of the request to a file of data/. */
  rest: Record<string, string>;
}

export interface Frame {
  /** The demo clock (RFC 3339) the frame was recorded at. */
  at: string;
  /** Topic name to a file of data/, in the order the stream sends them (core.plugins first). */
  topics: Record<string, string>;
}

export const MANIFEST_VERSION = 1;

/**
 * The key a GET is recorded and looked up by: the path, then the query with its parameters sorted
 * by name and value, so the order a caller adds them in never matters. The recorder computes the
 * same key (web/scripts/lib/demo-recording.mjs).
 */
export function recordingKey(pathname: string, search: URLSearchParams | string): string {
  const params = [...new URLSearchParams(search)];
  params.sort(([ak, av], [bk, bv]) => (ak === bk ? cmp(av, bv) : cmp(ak, bk)));
  const query = new URLSearchParams(params).toString();
  return query ? `${pathname}?${query}` : pathname;
}

function cmp(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/** Throws unless m is a manifest this player can replay. */
export function checkManifest(m: unknown): Manifest {
  const man = m as Partial<Manifest> | null;
  if (
    !man ||
    man.version !== MANIFEST_VERSION ||
    typeof man.stepSeconds !== "number" ||
    man.stepSeconds <= 0 ||
    !Array.isArray(man.frames) ||
    man.frames.length === 0 ||
    typeof man.rest !== "object" ||
    man.rest === null
  ) {
    throw new Error("the demo recording is missing or has an unknown format");
  }
  return man as Manifest;
}
