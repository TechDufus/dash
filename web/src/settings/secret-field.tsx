import { type KeyboardEvent, useEffect, useId, useRef, useState } from "react";
import type { SettingField } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { FieldRow } from "./fields/field-row";
import type { Leaf } from "./form-model";

export interface SecretFieldProps {
  leaf: Leaf;
  /** The typed value while the replace field is open; undefined while it is closed. */
  value: string | undefined;
  onOpen: () => void;
  onChange: (value: string) => void;
  /** Closes the replace field and forgets what was typed. */
  onClose: () => void;
  onClear: () => void;
  clearing: boolean;
  /** A destination leaf changed and this secret is set: it must be re-entered (design D16). */
  needsReentry: boolean;
  error?: string;
  onRemoveStored?: () => void;
  removing?: boolean;
}

/** "Set (ui)", "Set (env)", "Not set"; a Kubernetes reference is named, a value never is. */
function secretStatus(f: SettingField | undefined): string {
  if (!f?.set) return "Not set";
  return f.ref?.startsWith("k8s:") ? `Set (${f.source}: ${f.ref})` : `Set (${f.source})`;
}

/**
 * A write-only secret: never prefilled. Replace opens a password input whose value is saved (or
 * tested) with the form; Escape or Cancel closes it and forgets the value, and the form closes it
 * after a successful save, so the value is never in the DOM afterwards. Clear removes the stored
 * secret. A locked secret (env, file) offers neither.
 */
export function SecretField({
  leaf,
  value,
  onOpen,
  onChange,
  onClose,
  onClear,
  clearing,
  needsReentry,
  error,
  onRemoveStored,
  removing,
}: SecretFieldProps) {
  const f = leaf.field;
  const replaceRef = useRef<HTMLButtonElement>(null);
  const refocus = useRef(false);
  const open = value !== undefined;
  /** Clear asks once more before the DELETE: the stored secret cannot be brought back. */
  const [confirming, setConfirming] = useState(false);
  const clearRef = useRef<HTMLButtonElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);
  const wasConfirming = useRef(false);

  useEffect(() => {
    if (confirming) confirmRef.current?.focus();
    else if (wasConfirming.current) clearRef.current?.focus();
    wasConfirming.current = confirming;
  }, [confirming]);

  useEffect(() => {
    if (!open && refocus.current) {
      refocus.current = false;
      replaceRef.current?.focus();
    }
  }, [open]);

  const cancel = () => {
    refocus.current = true;
    onClose();
  };

  return (
    <FieldRow
      leaf={leaf}
      error={error}
      labelFor={false}
      onRemoveStored={onRemoveStored}
      removing={removing}
    >
      {(control) => (
        <fieldset
          aria-labelledby={control.labelId}
          aria-describedby={control["aria-describedby"]}
          className="space-y-2"
        >
          <p className="text-sm">{secretStatus(f)}</p>
          {needsReentry && !open && !error && (
            <p className="text-xs text-warn">The destination changed: re-enter this secret.</p>
          )}
          {open ? (
            <ReplaceInput
              leaf={leaf}
              value={value}
              invalid={control["aria-invalid"]}
              errorId={control.errorId}
              onChange={onChange}
              onCancel={cancel}
            />
          ) : (
            !f?.locked && (
              <div className="flex gap-2">
                <Button
                  ref={replaceRef}
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={onOpen}
                  aria-label={`Replace ${leaf.key}`}
                >
                  Replace
                </Button>
                {f?.set &&
                  (confirming ? (
                    <>
                      <Button
                        ref={confirmRef}
                        type="button"
                        variant="destructive"
                        size="sm"
                        disabled={clearing}
                        onClick={() => {
                          setConfirming(false);
                          onClear();
                        }}
                      >
                        Confirm clear {leaf.key}
                      </Button>
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => setConfirming(false)}
                        onKeyDown={(e) => {
                          if (e.key === "Escape") setConfirming(false);
                        }}
                        aria-label={`Cancel clearing ${leaf.key}`}
                      >
                        Cancel
                      </Button>
                    </>
                  ) : (
                    <Button
                      ref={clearRef}
                      type="button"
                      variant="outline"
                      size="sm"
                      disabled={clearing}
                      onClick={() => setConfirming(true)}
                      aria-label={`Clear ${leaf.key}`}
                    >
                      Clear
                    </Button>
                  ))}
              </div>
            )
          )}
        </fieldset>
      )}
    </FieldRow>
  );
}

function ReplaceInput({
  leaf,
  value,
  invalid,
  errorId,
  onChange,
  onCancel,
}: {
  leaf: Leaf;
  value: string;
  invalid?: true;
  errorId?: string;
  onChange: (value: string) => void;
  onCancel: () => void;
}) {
  const id = useId();
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => ref.current?.focus(), []);
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      e.preventDefault();
      onCancel();
    }
  };
  return (
    <div className="space-y-1.5">
      <Label htmlFor={id}>New {leaf.key}</Label>
      <div className="flex gap-2">
        <Input
          ref={ref}
          id={id}
          type="password"
          autoComplete="new-password"
          spellCheck={false}
          value={value}
          aria-invalid={invalid}
          aria-describedby={[errorId, `${id}-hint`].filter(Boolean).join(" ")}
          onChange={(e) => onChange(e.target.value)}
          onKeyDown={onKeyDown}
        />
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
      </div>
      <p id={`${id}-hint`} className="text-xs text-muted">
        A value, or a Kubernetes reference k8s:namespace/name#key. It is saved with the form; Escape
        cancels.
      </p>
    </div>
  );
}
