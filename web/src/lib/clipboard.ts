/**
 * Copies the text that `text` resolves to, and resolves to it once it is on the clipboard.
 *
 * Call it synchronously in the click handler, with the fetch still pending: the clipboard write
 * starts before anything is awaited, with a ClipboardItem holding the pending text, so Safari
 * keeps the user gesture across the fetch. Where the browser has no ClipboardItem, or refuses it,
 * the text goes through `writeText`; where the async clipboard API is missing too (a page served
 * over http on a LAN address is not a secure context), through a hidden textarea and
 * `document.execCommand("copy")`. A rejected `text` rejects with its own error.
 */
export async function copyText(text: Promise<string>): Promise<string> {
  const clipboard = globalThis.navigator?.clipboard;
  if (typeof clipboard?.write === "function" && typeof ClipboardItem === "function") {
    const blob = text.then((t) => new Blob([t], { type: "text/plain" }));
    try {
      await clipboard.write([new ClipboardItem({ "text/plain": blob })]);
      return await text;
    } catch {
      // A failed fetch is the error to report, not the clipboard's; otherwise try writeText.
      await text;
    }
  }
  const t = await text;
  if (typeof clipboard?.writeText === "function") {
    try {
      await clipboard.writeText(t);
      return t;
    } catch {
      // Fall through to the textarea.
    }
  }
  if (!copyWithTextarea(t)) throw new Error("the browser refused to copy");
  return t;
}

/** Copies text through a hidden textarea and execCommand; false when the browser refused. */
function copyWithTextarea(text: string): boolean {
  const before = document.activeElement;
  const area = document.createElement("textarea");
  area.value = text;
  area.readOnly = true;
  area.setAttribute("aria-hidden", "true");
  area.className = "fixed top-0 left-0 size-px opacity-0";
  // Inside an open dialog: its focus trap would pull focus, and the selection, back otherwise.
  (before?.closest("[role=dialog]") ?? document.body).append(area);
  area.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    area.remove();
    if (before instanceof HTMLElement) before.focus();
  }
}
