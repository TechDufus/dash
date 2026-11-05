import { describe, expect, it, type Mock, vi } from "vitest";
import { boot, registerServiceWorker, type ServiceWorkerHost } from "./boot";

function deps() {
  return {
    installChunkReload: vi.fn(),
    registerServiceWorker: vi.fn(),
    startStream: vi.fn(),
    prefetchPlugins: vi.fn(),
  };
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
      expect(d.registerServiceWorker).toHaveBeenCalledTimes(1);
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
      expect(d.registerServiceWorker).toHaveBeenCalledTimes(1);
    },
  );
});

describe("registerServiceWorker", () => {
  function host(secure: boolean, register?: Mock): ServiceWorkerHost {
    const navigator = register ? { serviceWorker: { register } } : {};
    return { isSecureContext: secure, navigator } as unknown as ServiceWorkerHost;
  }

  it("registers /sw.js for the whole origin in a secure context", () => {
    const register = vi.fn().mockResolvedValue({});
    expect(registerServiceWorker(host(true, register))).toBe(true);
    expect(register).toHaveBeenCalledExactlyOnceWith("/sw.js", { scope: "/" });
  });

  it("registers nothing outside a secure context", () => {
    const register = vi.fn().mockResolvedValue({});
    expect(registerServiceWorker(host(false, register))).toBe(false);
    expect(register).not.toHaveBeenCalled();
  });

  it("registers nothing in a browser without service workers", () => {
    expect(registerServiceWorker(host(true))).toBe(false);
  });

  it("swallows a failed registration with a warning", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    try {
      const register = vi.fn().mockRejectedValue(new Error("blocked"));
      expect(registerServiceWorker(host(true, register))).toBe(true);
      await vi.waitFor(() => expect(warn).toHaveBeenCalledTimes(1));
      expect(String(warn.mock.calls[0]?.[0])).toMatch(/service worker/);
    } finally {
      warn.mockRestore();
    }
  });
});
