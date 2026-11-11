import { ClipboardCopy } from "lucide-react";
import { useRef } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { copyElementText } from "@/lib/clipboard";

/**
 * A command or key to copy: its label, a Copy button and the text in a block that wraps. The copy
 * happens inside the click: writeText, else the shown text through execCommand (a page served
 * over plain http has no async clipboard), else the text stays there to copy by hand.
 */
export function CopyText({
  label,
  text,
  what = "Command",
}: {
  label: string;
  text: string;
  /** What the toast says was copied. */
  what?: string;
}) {
  const ref = useRef<HTMLElement>(null);
  function copy() {
    const refused = () =>
      toast.error(`Copying failed: select the ${what.toLowerCase()} and copy it yourself`);
    const clipboard = globalThis.navigator?.clipboard;
    if (typeof clipboard?.writeText === "function") {
      clipboard.writeText(text).then(() => toast.success(`${what} copied`), refused);
    } else if (ref.current && copyElementText(ref.current)) {
      toast.success(`${what} copied`);
    } else {
      refused();
    }
  }
  return (
    <div className="min-w-0 space-y-1">
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs text-muted">{label}</span>
        <Button type="button" variant="outline" size="xs" onClick={copy}>
          <ClipboardCopy aria-hidden="true" />
          Copy <span className="sr-only">{label}</span>
        </Button>
      </div>
      <pre className="rounded-lg border border-line bg-panel-hover p-3 text-xs whitespace-pre-wrap wrap-anywhere">
        <code ref={ref}>{text}</code>
      </pre>
    </div>
  );
}
