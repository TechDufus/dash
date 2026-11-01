const KEY = "dash:chunk-reload";

/**
 * index.html is no-cache and assets/* are removed with the old image, so a tab loaded before a
 * deploy that opens a page it had not visited asks for a chunk that 404s. Vite reports it as
 * `vite:preloadError`; one reload fetches the new index.html. A second failure within 30 s is
 * left alone (the same guard as the 401 reload in api/client.ts), so a missing chunk never loops.
 * `reload` is a parameter because jsdom's location cannot be replaced in a test.
 */
export function installChunkReload(
  reload: () => void = () => window.location.reload(),
): () => void {
  const onError = (e: Event) => {
    e.preventDefault();
    const last = Number(sessionStorage.getItem(KEY) ?? 0);
    if (Date.now() - last < 30_000) return;
    sessionStorage.setItem(KEY, String(Date.now()));
    reload();
  };
  window.addEventListener("vite:preloadError", onError);
  return () => window.removeEventListener("vite:preloadError", onError);
}
