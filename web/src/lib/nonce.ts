// The server replaces the placeholder in index.html's `csp-nonce` meta with a fresh per-response
// nonce. `vite dev`, the tests and any server that does not template the file still carry the
// placeholder, which is not a nonce.
const PLACEHOLDER = "__CSP_NONCE__";

/** The page's CSP nonce, or undefined when the meta is absent, empty or still the placeholder. */
export function cspNonce(): string | undefined {
  const meta = document.querySelector<HTMLMetaElement>('meta[name="csp-nonce"]');
  const value = meta?.getAttribute("content")?.trim();
  return value && value !== PLACEHOLDER ? value : undefined;
}
