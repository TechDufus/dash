import { useQueryClient } from "@tanstack/react-query";
import { type FormEvent, type JSX, useState } from "react";
import { toast } from "sonner";
import { api, errorMessage } from "@/api/client";
import type { SettingField, SettingsView } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { fieldErrors, invalidateSettings, settingsPaths } from "./api";
import { lockedText, sourceText } from "./list-locked";

/** The one general key the UI may write (contract §4.6: every other root key is ui-ineligible). */
export const ICON_BASE_URL = "ui.iconBaseURL";

function display(f: SettingField): string {
  if (f.secret) return f.set ? "set" : "not set";
  const v = f.value;
  if (v === undefined || v === null || v === "") return "(empty)";
  if (f.path === "auth.tokens" && Array.isArray(v)) return staticTokens(v);
  if (Array.isArray(v)) return v.length === 0 ? "(none)" : v.map(String).join(", ");
  if (typeof v === "object") return JSON.stringify(v);
  return String(v);
}

/**
 * auth.tokens as a summary per entry, "ci (viewer, api, read)": what each static token may do,
 * never its hash.
 */
export function staticTokens(entries: unknown[]): string {
  if (entries.length === 0) return "(none)";
  return entries
    .map((e) => {
      const t = (e ?? {}) as {
        name?: unknown;
        role?: unknown;
        audience?: unknown;
        scopes?: unknown;
      };
      const scopes = Array.isArray(t.scopes) ? t.scopes.map(String).join(" ") : "";
      return `${String(t.name ?? "?")} (${[t.role, t.audience, scopes].map((x) => String(x ?? "")).join(", ")})`;
    })
    .join("; ");
}

/**
 * The general section: server.*, auth.*, settings.*, dataDir as read-only information with their
 * provenance; ui.iconBaseURL is editable (through PUT general) unless it is locked.
 */
export function GeneralSection({ view }: { view: SettingsView }): JSX.Element {
  return (
    <section aria-labelledby="settings-general" className="space-y-2">
      <h2 id="settings-general" className="text-sm font-semibold text-fg">
        General
      </h2>
      <dl className="divide-y divide-line rounded-lg border border-line bg-panel">
        {view.general.map((f) =>
          f.path === ICON_BASE_URL && !f.locked && !view.store.readonly ? (
            // Remounted per revision, so a refetch after a save resets the draft.
            <IconBaseURL key={`${f.path}@${view.revision}`} field={f} revision={view.revision} />
          ) : (
            <div key={f.path} className="grid gap-1 px-4 py-2 sm:grid-cols-[16rem_1fr_auto]">
              <dt className="font-mono text-xs text-muted">{f.path}</dt>
              <dd className="text-sm break-all text-fg">{display(f)}</dd>
              <dd className="text-xs text-faint" title={lockedText(f)}>
                {sourceText(f)}
                {f.restart ? " · restart" : ""}
              </dd>
            </div>
          ),
        )}
      </dl>
    </section>
  );
}

function IconBaseURL({ field, revision }: { field: SettingField; revision: number }) {
  const queryClient = useQueryClient();
  const initial = typeof field.value === "string" ? field.value : "";
  const [value, setValue] = useState(initial);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);
  const inputId = "settings-ui-iconBaseURL";
  const errorId = `${inputId}-error`;
  const noteId = `${inputId}-note`;

  async function save(e: FormEvent) {
    e.preventDefault();
    if (pending) return;
    setPending(true);
    setError(null);
    try {
      await api.put(settingsPaths.general, { ui: { iconBaseURL: value } }, { ifMatch: revision });
      toast.success("Saved: ui.iconBaseURL applies after a restart");
    } catch (err) {
      const msg = fieldErrors(err).get(ICON_BASE_URL);
      if (msg) setError(msg);
      else toast.error(errorMessage(err));
    } finally {
      // After a failure too: a 412 leaves the next attempt the fresh revision.
      await invalidateSettings(queryClient);
      setPending(false);
    }
  }

  return (
    <div className="grid items-start gap-1 px-4 py-2 sm:grid-cols-[16rem_1fr_auto]">
      <dt>
        <Label htmlFor={inputId} className="font-mono text-xs font-normal text-muted">
          {field.path}
        </Label>
      </dt>
      <dd>
        <form onSubmit={save} className="space-y-1">
          <div className="flex gap-2">
            <Input
              id={inputId}
              name="iconBaseURL"
              type="text"
              inputMode="url"
              value={value}
              aria-invalid={error ? true : undefined}
              aria-describedby={error ? `${errorId} ${noteId}` : noteId}
              onChange={(e) => setValue(e.target.value)}
            />
            <Button type="submit" size="sm" disabled={pending || value === initial}>
              Save
            </Button>
          </div>
          {error ? (
            <p id={errorId} role="alert" className="text-xs text-crit">
              {error}
            </p>
          ) : null}
          <p id={noteId} className="text-xs text-faint">
            Applies after a restart.
          </p>
        </form>
      </dd>
      <dd className="text-xs text-faint">{sourceText(field)}</dd>
    </div>
  );
}
