/** Result of resolving a JSON pointer: `found` is false for a missing path or a malformed pointer. */
export type PointerResult = { found: true; value: unknown } | { found: false };

const NOT_FOUND: PointerResult = { found: false };
const INDEX = /^(0|[1-9][0-9]*)$/;
/** A "~" that is not "~0" or "~1" makes the whole pointer malformed (RFC 6901 section 3). */
const BAD_ESCAPE = /~(?![01])/;

/**
 * Resolves an RFC 6901 JSON pointer against `doc`. "" is the whole document; every other pointer
 * starts with "/" and has its "~1" and "~0" escapes decoded, in that order; any other "~" makes
 * the pointer malformed, which is not found. Arrays take decimal indexes without leading zeros
 * ("-" never resolves); objects only their own properties.
 */
export function resolvePointer(doc: unknown, pointer: string): PointerResult {
  if (pointer === "") return { found: true, value: doc };
  if (!pointer.startsWith("/") || BAD_ESCAPE.test(pointer)) return NOT_FOUND;
  let cur: unknown = doc;
  for (const raw of pointer.slice(1).split("/")) {
    const token = raw.replace(/~1/g, "/").replace(/~0/g, "~");
    if (Array.isArray(cur)) {
      if (!INDEX.test(token)) return NOT_FOUND;
      const i = Number(token);
      if (i >= cur.length) return NOT_FOUND;
      cur = cur[i];
    } else if (typeof cur === "object" && cur !== null) {
      if (!Object.hasOwn(cur, token)) return NOT_FOUND;
      cur = (cur as Record<string, unknown>)[token];
    } else {
      return NOT_FOUND;
    }
  }
  return { found: true, value: cur };
}
