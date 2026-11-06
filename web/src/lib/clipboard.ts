/**
 * The async clipboard is missing or refused the text: a page served over http on a LAN address is
 * not a secure context, and a browser may refuse a write that comes after an await. The caller
 * shows `text` for a copy inside a click of its own (copyFromTextarea).
 */
export class ClipboardUnavailableError extends Error {
  constructor(readonly text: string) {
    super("the clipboard is not available on this page");
    this.name = "ClipboardUnavailableError";
  }
}

/**
 * Copies the text that `text` resolves to, and resolves to it once it is on the clipboard.
 *
 * Call it synchronously in the click handler, with the fetch still pending: the clipboard write
 * starts before anything is awaited, with a ClipboardItem holding the pending text, so Safari
 * keeps the user gesture across the fetch. Where the browser has no ClipboardItem, or refuses it,
 * the text goes through `writeText`. Where that is missing or refused too, it rejects with a
 * ClipboardUnavailableError holding the text. A rejected `text` rejects with its own error.
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
      // Refused: the caller offers the text for a copy in a click of its own.
    }
  }
  throw new ClipboardUnavailableError(t);
}

/**
 * Selects all of area and copies it with `document.execCommand("copy")`; false when the browser
 * refused. Call it synchronously in a click handler: execCommand copies only in a user gesture.
 */
export function copyFromTextarea(area: HTMLTextAreaElement): boolean {
  area.focus();
  area.select();
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  }
}

/**
 * Selects the text of el and copies it with `document.execCommand("copy")`; false when the browser
 * refused, with the text left selected for a manual copy. Call it synchronously in a click handler.
 */
export function copyElementText(el: HTMLElement): boolean {
  const selection = globalThis.getSelection?.();
  if (!selection) return false;
  selection.selectAllChildren(el);
  try {
    return document.execCommand("copy");
  } catch {
    return false;
  }
}
