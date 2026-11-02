import { cspNonce } from "@/lib/nonce";

// Side-effect only, and the FIRST import of main.tsx: ES modules evaluate in import order, so this
// runs before react-style-singleton (via react-remove-scroll) or any other import's top level can
// read `__webpack_nonce__` (the global `get-nonce` reads) to stamp the <style> elements they inject.
declare global {
  interface Window {
    __webpack_nonce__?: string;
  }
}

const nonce = cspNonce();
if (nonce) {
  window.__webpack_nonce__ = nonce;
}
