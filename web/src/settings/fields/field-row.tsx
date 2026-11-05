import { Lock } from "lucide-react";
import { type ReactNode, useId, useState } from "react";
import type { SettingField } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import type { Leaf } from "../form-model";

/** What a row hands its control: the ids that tie it to its label, help text and error. */
export interface ControlProps {
  id: string;
  labelId: string;
  /** The id of the error under the field, when there is one. */
  errorId?: string;
  "aria-describedby": string;
  "aria-invalid"?: true;
}

/** Why a locked field cannot be edited here: the layer that sets it and its variable or file. */
export function lockText(f: SettingField): string {
  switch (f.lockedBy) {
    case "env":
      return f.ref ? `Locked: set by env ${f.ref}` : "Locked: set by env";
    case "file":
      return f.ref ? `Locked: set in file ${f.ref}` : "Locked: set in a file";
    case "ui-ineligible":
      return "Locked: not settable from the UI";
    case "readonly":
      return "Locked: settings are read-only";
    default:
      return "Locked";
  }
}

export interface FieldRowProps {
  leaf: Leaf;
  error?: string;
  /** False for a control without one labelable element (a secret's group): the label is a span the
   * control references with aria-labelledby. */
  labelFor?: boolean;
  /** Shown for a shadowed field: DELETE the stored value. */
  onRemoveStored?: () => void;
  removing?: boolean;
  /** Shown for a stored, unlocked field: DELETE the stored value so the default applies. */
  onResetDefault?: () => void;
  children: (control: ControlProps) => ReactNode;
}

/**
 * One leaf: its label, the control, the doc, where the value comes from (or what locks it), and
 * the error under it. The control's aria-describedby names the error, the provenance line and the
 * doc, in that order.
 */
export function FieldRow({
  leaf,
  error,
  labelFor = true,
  onRemoveStored,
  removing,
  onResetDefault,
  children,
}: FieldRowProps) {
  const id = useId();
  const f = leaf.field;
  const ids = { label: `${id}-label`, error: `${id}-error`, info: `${id}-info`, doc: `${id}-doc` };
  const info = f?.locked
    ? lockText(f)
    : f?.source === "ui"
      ? "Saved from the UI"
      : f?.set
        ? undefined
        : "Default";
  const describedBy = [
    error && ids.error,
    (info || f?.restart) && ids.info,
    f?.shadowed && `${id}-shadow`,
    leaf.doc && ids.doc,
  ]
    .filter(Boolean)
    .join(" ");
  const control = children({
    id,
    labelId: ids.label,
    errorId: error ? ids.error : undefined,
    "aria-describedby": describedBy,
    ...(error ? { "aria-invalid": true as const } : {}),
  });
  return (
    <div className="min-w-0 space-y-1.5" data-path={leaf.path}>
      {labelFor ? (
        <Label id={ids.label} htmlFor={id} className="font-mono">
          {leaf.key}
        </Label>
      ) : (
        <span id={ids.label} className="font-mono text-sm leading-none font-medium">
          {leaf.key}
        </span>
      )}
      {f?.locked && (leaf.kind === "boolean" || leaf.kind === "enum") ? (
        // A disabled switch or select takes no focus: the lock and its reason stay reachable here,
        // beside the control.
        <div className="flex min-w-0 items-center gap-2">
          {control}
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                aria-label={lockText(f)}
                className="inline-flex shrink-0 rounded-sm text-faint focus-visible:outline-2 focus-visible:outline-accent"
              >
                <Lock className="size-3.5" aria-hidden="true" />
              </button>
            </TooltipTrigger>
            <TooltipContent>{lockText(f)}</TooltipContent>
          </Tooltip>
        </div>
      ) : (
        control
      )}
      {error && (
        <p id={ids.error} className="text-xs text-destructive">
          {error}
        </p>
      )}
      {(info || f?.restart) && (
        <p
          id={ids.info}
          className={f?.locked ? "text-xs break-all text-muted" : "text-xs text-muted"}
        >
          {[info, f?.restart && "applies after a restart"].filter(Boolean).join(" · ")}
        </p>
      )}
      {onResetDefault && f?.stored && !f.locked && (
        <Button
          type="button"
          variant="outline"
          size="xs"
          disabled={removing}
          onClick={onResetDefault}
        >
          {/* Named by its content, not aria-label, so a lookup of the field by label finds the
              input alone. */}
          Reset <span className="sr-only">{leaf.key}</span> to default
        </Button>
      )}
      {f?.shadowed && (
        <div className="flex flex-wrap items-center gap-2">
          <p id={`${id}-shadow`} className="text-xs text-warn">
            overridden by {f.source}; stored value unused
          </p>
          {onRemoveStored && (
            <Button
              type="button"
              variant="outline"
              size="xs"
              disabled={removing}
              onClick={onRemoveStored}
              aria-label={`Remove stored value of ${leaf.key}`}
            >
              Remove stored value
            </Button>
          )}
        </div>
      )}
      {leaf.doc && <Doc id={ids.doc} text={leaf.doc} name={leaf.key} />}
    </div>
  );
}

/** Docs longer than this show their first sentence, the rest behind "More". */
const LONG_DOC = 160;

/** The index of the mark that ends the first sentence of `text`, or -1: a `.`, `!` or `?` followed by
 * a space and an upper-case letter, or by the end, and not closing an abbreviation such as "e.g.". */
export function firstSentenceEnd(text: string): number {
  for (const m of text.matchAll(/[.!?](?=\s+[A-Z]|\s*$)/g)) {
    if (/(?:^|[\s(])(?:e\.g|i\.e|etc|vs)\.$/i.test(text.slice(0, m.index + 1))) continue;
    return m.index;
  }
  return -1;
}

function Doc({ id, text, name }: { id: string; text: string; name: string }) {
  const [open, setOpen] = useState(false);
  const restId = `${id}-rest`;
  const cut = text.length > LONG_DOC ? firstSentenceEnd(text) : -1;
  if (cut < 0 || cut + 1 >= text.trimEnd().length) {
    return (
      <p id={id} className="text-xs text-muted">
        {text}
      </p>
    );
  }
  return (
    <div className="space-y-1">
      <p id={id} className="text-xs text-muted">
        {text.slice(0, cut + 1)}
        {open && <span id={restId}>{text.slice(cut + 1)}</span>}
      </p>
      <Button
        type="button"
        variant="link"
        size="xs"
        className="h-auto px-0"
        aria-expanded={open}
        aria-controls={open ? restId : undefined}
        onClick={() => setOpen((o) => !o)}
      >
        {open ? "Less" : "More"} <span className="sr-only">about {name}</span>
      </Button>
    </div>
  );
}
