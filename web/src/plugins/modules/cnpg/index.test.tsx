import { describe, expect, it } from "vitest";
import cnpg from "./index";

describe("the cnpg module", () => {
  it("declares its one topic and draws no page", () => {
    expect(cnpg.id).toBe("cnpg");
    expect(Object.keys(cnpg.topics)).toEqual(["cnpg.clusters"]);
    expect(cnpg.routes).toBeUndefined();
  });
});
