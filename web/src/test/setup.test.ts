import { describe, expect, it, vi } from "vitest";

// The order matters here, so the suite opts out of --sequence.shuffle.
describe("test setup", { shuffle: false }, () => {
  it("lets a test spy on console.error", () => {
    vi.spyOn(console, "error").mockImplementation(() => {});
    expect(vi.isMockFunction(console.error)).toBe(true);
  });

  it("restores that spy before the next test", () => {
    expect(vi.isMockFunction(console.error)).toBe(false);
  });
});
