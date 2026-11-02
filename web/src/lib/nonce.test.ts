import { afterEach, describe, expect, it, vi } from "vitest";
import { cspNonce } from "@/lib/nonce";

function setMeta(content: string | null) {
  const meta = document.createElement("meta");
  meta.name = "csp-nonce";
  if (content !== null) meta.setAttribute("content", content);
  document.head.appendChild(meta);
}

// The setup module acts at import time, so each case imports a fresh copy.
async function runSetup() {
  vi.resetModules();
  await import("@/lib/nonce-setup");
}

afterEach(() => {
  document.head.querySelectorAll('meta[name="csp-nonce"]').forEach((m) => {
    m.remove();
  });
  delete window.__webpack_nonce__;
});

describe("cspNonce", () => {
  it("is undefined for the placeholder the dev server and the tests see", () => {
    setMeta("__CSP_NONCE__");
    expect(cspNonce()).toBeUndefined();
  });

  it("is undefined without the meta and for an empty content", () => {
    expect(cspNonce()).toBeUndefined();
    setMeta("");
    expect(cspNonce()).toBeUndefined();
  });

  it("is undefined when the meta has no content attribute", () => {
    setMeta(null);
    expect(cspNonce()).toBeUndefined();
  });

  it("returns the server's nonce", () => {
    setMeta("dGVzdC1ub25jZS0xMjM0NQ");
    expect(cspNonce()).toBe("dGVzdC1ub25jZS0xMjM0NQ");
  });
});

describe("nonce-setup", () => {
  it("sets window.__webpack_nonce__ from a real nonce", async () => {
    setMeta("dGVzdC1ub25jZS0xMjM0NQ");
    await runSetup();
    expect(window.__webpack_nonce__).toBe("dGVzdC1ub25jZS0xMjM0NQ");
  });

  it("leaves window.__webpack_nonce__ unset for the placeholder", async () => {
    setMeta("__CSP_NONCE__");
    await runSetup();
    expect(window.__webpack_nonce__).toBeUndefined();
  });

  it("leaves window.__webpack_nonce__ unset without the meta", async () => {
    await runSetup();
    expect(window.__webpack_nonce__).toBeUndefined();
  });
});
