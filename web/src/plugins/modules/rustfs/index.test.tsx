import { describe, expect, it } from "vitest";
import rustfs from "./index";

describe("the rustfs module", () => {
  it("declares its one topic and draws no page", () => {
    expect(rustfs.id).toBe("rustfs");
    expect(Object.keys(rustfs.topics)).toEqual(["rustfs.usage"]);
    expect(rustfs.routes).toBeUndefined();
  });
});
