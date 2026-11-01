import { describe, expect, it } from "vitest";
import { resolvePointer } from "./pointer";

// The document and the pointer table of RFC 6901 section 5.
const doc = {
  foo: ["bar", "baz"],
  "": 0,
  "a/b": 1,
  "c%d": 2,
  "e^f": 3,
  "g|h": 4,
  "i\\j": 5,
  'k"l': 6,
  " ": 7,
  "m~n": 8,
};

describe("resolvePointer", () => {
  it.each([
    ["", doc],
    ["/foo", ["bar", "baz"]],
    ["/foo/0", "bar"],
    ["/", 0],
    ["/a~1b", 1],
    ["/c%d", 2],
    ["/e^f", 3],
    ["/g|h", 4],
    ["/i\\j", 5],
    ['/k"l', 6],
    ["/ ", 7],
    ["/m~0n", 8],
  ])("resolves %j as in the RFC", (pointer, want) => {
    expect(resolvePointer(doc, pointer)).toEqual({ found: true, value: want });
  });

  it("decodes ~1 before ~0, so ~01 is the literal ~1", () => {
    expect(resolvePointer({ "~1": "tilde-one", "/": "slash" }, "/~01")).toEqual({
      found: true,
      value: "tilde-one",
    });
  });

  it("finds a null value; only a missing path is not found", () => {
    expect(resolvePointer({ a: null }, "/a")).toEqual({ found: true, value: null });
    expect(resolvePointer({ a: null }, "/b")).toEqual({ found: false });
  });

  it.each([
    ["a/b", "no leading slash"],
    ["/foo/2", "index past the end"],
    ["/foo/-", "the append marker"],
    ["/foo/01", "leading zero"],
    ["/foo/one", "not an index"],
    ["/foo/0/x", "through a string"],
    ["/nope", "missing key"],
    ["/toString", "an inherited property"],
    ["/foo/length", "an array property"],
  ])("does not resolve %j (%s)", (pointer) => {
    expect(resolvePointer(doc, pointer)).toEqual({ found: false });
  });

  it.each(["/a~2b", "/a~", "/a~x", "/a~~0", "/~2/x"])(
    "does not resolve the malformed pointer %j, even when the key exists literally",
    (pointer) => {
      const literal = { "a~2b": 1, "a~": 2, "a~x": 3, "a~~0": 4, "~2": { x: 5 } };
      expect(resolvePointer(literal, pointer)).toEqual({ found: false });
    },
  );

  it("still resolves the escapes ~0 and ~1 next to other text", () => {
    expect(resolvePointer({ "a~b/c": 1 }, "/a~0b~1c")).toEqual({ found: true, value: 1 });
  });
});
