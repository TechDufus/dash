import { describe, expect, it, vi } from "vitest";
import { boot } from "./boot";

function deps() {
  return { installChunkReload: vi.fn(), startStream: vi.fn(), prefetchPlugins: vi.fn() };
}

describe("boot", () => {
  it.each(["/login", "/setup", "/login/x", "/setup/again"])(
    "gives %s the public shell without the stream or the plugin list",
    (path) => {
      const d = deps();
      expect(boot(path, d)).toBe("public");
      expect(d.startStream).not.toHaveBeenCalled();
      expect(d.prefetchPlugins).not.toHaveBeenCalled();
      expect(d.installChunkReload).toHaveBeenCalledTimes(1);
    },
  );

  it.each(["/", "/alerts", "/loginx", "/settings/setup"])(
    "gives %s the app, which starts the stream and asks for the plugin list",
    (path) => {
      const d = deps();
      expect(boot(path, d)).toBe("app");
      expect(d.startStream).toHaveBeenCalledTimes(1);
      expect(d.prefetchPlugins).toHaveBeenCalledTimes(1);
      expect(d.installChunkReload).toHaveBeenCalledTimes(1);
    },
  );
});
