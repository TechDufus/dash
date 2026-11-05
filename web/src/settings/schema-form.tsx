import { type FormEvent, useMemo, useState } from "react";
import { ApiError, errorMessage } from "@/api/client";
import type { CheckReport, SettingsPluginView } from "@/api/types";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { fieldErrors, settingsErrorDetails } from "./api";
import { FieldRow } from "./fields/field-row";
import { LeafControl } from "./fields/leaf-control";
import {
  buildTree,
  type Changes,
  collect,
  type FormNode,
  initialRaw,
  isK8sRef,
  type Leaf,
  leafForPath,
  leavesOf,
  type Raw,
} from "./form-model";
import { SecretField } from "./secret-field";
import { useFieldDelete, useSave, useTestConnection } from "./use-save";

/** Ruling 6: a new k8s: reference has no value until it is saved, so its test is not_configured. */
export const K8S_SAVE_FIRST = "save first to test a new Kubernetes secret reference";

interface FormError {
  message: string;
  /** Problems no field of the form carries, as "path: message". */
  items: string[];
}

/**
 * The generic plugin settings form (P4.9), driven by the served JSON Schema: every leaf with its
 * provenance, locked leaves read-only, secrets write-only. Save writes the pending changes with
 * If-Match of the view's revision; Test connection (when the plugin is testable) tries them
 * without saving. Errors render under their field (aria-describedby), the rest in one alert.
 */
export function SchemaForm({ view }: { view: SettingsPluginView }) {
  const tree = useMemo(() => buildTree(view), [view]);
  const leaves = useMemo(() => leavesOf(tree), [tree]);
  const [raw, setRaw] = useState<Record<string, Raw>>({});
  const [secrets, setSecrets] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<ReadonlyMap<string, string>>(new Map());
  const [formError, setFormError] = useState<FormError>();
  const [status, setStatus] = useState<string>();
  const [report, setReport] = useState<{ checks: CheckReport["checks"]; saveFirst: boolean }>();
  const save = useSave(view.id);
  const test = useTestConnection(view.id);
  const del = useFieldDelete(view.id);
  const busy = save.isPending || test.isPending || del.isPending;
  const editable = leaves.some((l) => !l.field?.locked);
  const destMoved = collect(leaves, raw, {}).changes.destChanged;

  const reset = () => {
    setErrors(new Map());
    setFormError(undefined);
    setStatus(undefined);
    setReport(undefined);
  };

  /** The server's refusal, each problem under its field when one carries it. */
  const showError = (e: unknown) => {
    const d = settingsErrorDetails(e);
    const found = new Map(fieldErrors(e));
    let message = errorMessage(e);
    if (d?.reason === "secret_locked" && d.path) {
      found.set(d.path, "Set outside the UI, so it cannot be re-entered here.");
      message = `${d.path} is set outside the UI (env or file), so the destination cannot change here: change it where that secret is set.`;
    } else if (d?.path && d.source) {
      found.set(d.path, `Locked: set by ${d.source}${d.ref ? ` ${d.ref}` : ""}`);
    } else if (e instanceof ApiError && e.code === "precondition_failed") {
      message =
        "The settings changed since this page read them. It now shows the current settings: check your changes and save again.";
    }
    const byLeaf = new Map<string, string>();
    const items: string[] = [];
    for (const [path, msg] of found) {
      const leaf = leafForPath(leaves, path);
      if (leaf && !byLeaf.has(leaf.path)) {
        const inner = path.slice(leaf.path.length).replace(/^\./, "");
        byLeaf.set(leaf.path, inner ? `${inner}: ${msg}` : msg);
      } else {
        items.push(`${path}: ${msg}`);
      }
    }
    setErrors(byLeaf);
    setFormError({ message, items });
  };

  /** The changes to send, or undefined after showing why they cannot be sent. */
  const pending = (): Changes | undefined => {
    reset();
    const c = collect(leaves, raw, secrets);
    if (c.errors.size > 0) {
      setErrors(c.errors);
      setFormError({ message: "Some fields need attention.", items: [] });
      return undefined;
    }
    return c.changes;
  };

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    const changes = pending();
    if (!changes) return;
    if (Object.keys(changes.patch).length + Object.keys(changes.secrets).length === 0) {
      setStatus("Nothing to save.");
      return;
    }
    try {
      const result = await save.mutateAsync({ revision: view.revision, changes });
      setRaw({});
      setSecrets({});
      const restart = result?.restartRequired.filter((p) => p.startsWith(`plugins.${view.id}.`));
      setStatus(restart?.length ? `Saved. Restart required: ${restart.join(", ")}` : "Saved.");
    } catch (err) {
      showError(err);
    }
  };

  const onTest = async () => {
    const changes = pending();
    if (!changes) return;
    try {
      const r = await test.mutateAsync(changes);
      const typedRef = Object.values(changes.secrets).some(isK8sRef);
      setReport({
        checks: r.checks,
        saveFirst: typedRef && r.checks.some((c) => c.category === "not_configured"),
      });
    } catch (err) {
      showError(err);
    }
  };

  const onDelete = (what: "secret" | "stored", leaf: Leaf) => {
    reset();
    del.mutate(
      { what, rel: leaf.rel, revision: view.revision },
      {
        onSuccess: () => setStatus(what === "secret" ? "Secret cleared." : "Stored value removed."),
        onError: showError,
      },
    );
  };

  const setLeaf = (leaf: Leaf, value: Raw) => setRaw((r) => ({ ...r, [leaf.rel]: value }));

  const setSecret = (leaf: Leaf, value: string | undefined) =>
    setSecrets(({ [leaf.rel]: _, ...rest }) =>
      value === undefined ? rest : { ...rest, [leaf.rel]: value },
    );

  const renderLeaf = (leaf: Leaf) => {
    // One instance per type: the plugin's type is not the user's to change (the server checks it).
    if (leaf.rel === "type") return null;
    const f = leaf.field;
    const onRemoveStored = f?.shadowed ? () => onDelete("stored", leaf) : undefined;
    const onResetDefault =
      f?.stored && !f.locked
        ? () => {
            setRaw(({ [leaf.rel]: _, ...rest }) => rest);
            onDelete("stored", leaf);
          }
        : undefined;
    if (leaf.kind === "secret") {
      return (
        <SecretField
          key={leaf.rel}
          leaf={leaf}
          value={secrets[leaf.rel]}
          onOpen={() => setSecret(leaf, "")}
          onChange={(v) => setSecret(leaf, v)}
          onClose={() => setSecret(leaf, undefined)}
          onClear={() => onDelete("secret", leaf)}
          clearing={del.isPending}
          needsReentry={destMoved && f?.set === true && !f.locked}
          error={errors.get(leaf.path)}
          onRemoveStored={onRemoveStored}
          removing={del.isPending}
        />
      );
    }
    return (
      <FieldRow
        key={leaf.rel}
        leaf={leaf}
        error={errors.get(leaf.path)}
        onRemoveStored={onRemoveStored}
        removing={del.isPending}
        onResetDefault={onResetDefault}
      >
        {(control) => (
          <LeafControl
            leaf={leaf}
            value={raw[leaf.rel] ?? initialRaw(leaf)}
            onChange={(v) => setLeaf(leaf, v)}
            control={control}
          />
        )}
      </FieldRow>
    );
  };

  const renderNodes = (nodes: readonly FormNode[]) =>
    nodes.map((n) =>
      n.node === "leaf" ? (
        renderLeaf(n)
      ) : (
        <fieldset key={n.rel} className="min-w-0 space-y-4 rounded-lg border border-line p-3">
          <legend className="px-1 font-mono text-sm font-medium">{n.key}</legend>
          {n.doc && <p className="text-xs text-muted">{n.doc}</p>}
          {renderNodes(n.children)}
        </fieldset>
      ),
    );

  return (
    <form noValidate onSubmit={onSubmit} aria-label={`${view.id} settings`} className="space-y-5">
      {formError && (
        <Alert variant="destructive">
          <AlertDescription>
            <p>{formError.message}</p>
            {formError.items.length > 0 && (
              <ul className="list-disc pl-4">
                {formError.items.map((i) => (
                  <li key={i}>{i}</li>
                ))}
              </ul>
            )}
          </AlertDescription>
        </Alert>
      )}
      {renderNodes(tree)}
      <div className="flex flex-wrap items-center gap-2">
        {view.testable && (
          <Button type="button" variant="outline" disabled={busy} onClick={onTest}>
            Test connection
          </Button>
        )}
        {editable && (
          <Button type="submit" disabled={busy}>
            Save
          </Button>
        )}
        <p role="status" className="text-sm text-muted">
          {status}
        </p>
      </div>
      {report && (
        <section aria-label="Connection test" className="space-y-1 text-sm">
          <ul>
            {report.checks.map((c) => (
              <li key={c.name} className={c.ok ? "text-ok" : "text-crit"}>
                {c.name}:{" "}
                {c.ok
                  ? `ok (${c.latencyMs} ms)`
                  : `${c.category ?? "failed"}${c.message ? `: ${c.message}` : ""}`}
              </li>
            ))}
          </ul>
          {report.saveFirst && <p className="text-warn">{K8S_SAVE_FIRST}</p>}
        </section>
      )}
    </form>
  );
}
