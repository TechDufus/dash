import { Puzzle, Server } from "lucide-react";
import { describe, expect, it } from "vitest";
import { iconFor } from "./icons";

describe("iconFor", () => {
  it("returns the icon of a known lucide name", () => {
    expect(iconFor("Server")).toBe(Server);
  });

  it.each([undefined, "", "NoSuchIcon", "constructor", "toString", "__proto__", "hasOwnProperty"])(
    "falls back to Puzzle for %j, never an inherited object property",
    (name) => {
      expect(iconFor(name)).toBe(Puzzle);
    },
  );
});
