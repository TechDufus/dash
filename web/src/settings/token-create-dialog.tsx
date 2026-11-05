import { useQueryClient } from "@tanstack/react-query";
import { Copy, Loader2, TriangleAlert } from "lucide-react";
import { type FormEvent, type JSX, useState } from "react";
import { toast } from "sonner";
import { errorMessage } from "@/api/client";
import {
  TokenAudienceAPI,
  TokenAudienceMCP,
  type TokenCreated,
  type TokenCreateRequest,
  TokenExpiryDefaultDays,
  TokenExpiryMaxDays,
  TokenNameMaxLen,
  TokenScopeActionsPrefix,
  TokenScopeLogs,
  TokenScopeRead,
} from "@/api/types";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { fieldErrors } from "./api";
import { createToken, invalidateTokens, useActionScopes } from "./tokens-api";

const AUDIENCES = [
  { value: TokenAudienceAPI, label: "api", note: "the REST API (/api/v1)" },
  { value: TokenAudienceMCP, label: "mcp", note: "agents (/mcp)" },
];

const BASE_SCOPES = [
  { value: TokenScopeRead, note: "read every page and topic" },
  { value: TokenScopeLogs, note: "read and tail logs" },
];

/** A field's error text under its input, tied to it by aria-describedby. */
function FieldError({ id, message }: { id: string; message?: string }) {
  if (!message) return null;
  return (
    <p id={id} role="alert" className="text-xs text-crit">
      {message}
    </p>
  );
}

export interface TokenCreateDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

/**
 * The create dialog: name, audience, scopes, action namespaces and expiry, then the new token,
 * shown this once. The form and the secret live in a body that mounts with the open dialog, so
 * closing it drops the secret from memory; nothing else ever holds it.
 */
export function TokenCreateDialog({ open, onOpenChange }: TokenCreateDialogProps): JSX.Element {
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open={open} onOpenChange={(o) => !busy && onOpenChange(o)}>
      <DialogContent className="sm:max-w-lg">
        {open ? <CreateBody onBusy={setBusy} onClose={() => onOpenChange(false)} /> : null}
      </DialogContent>
    </Dialog>
  );
}

function CreateBody({
  onBusy,
  onClose,
}: {
  onBusy: (busy: boolean) => void;
  onClose: () => void;
}): JSX.Element {
  const [created, setCreated] = useState<TokenCreated | null>(null);
  if (created) return <ShownOnce created={created} onClose={onClose} />;
  return <CreateForm onBusy={onBusy} onCreated={setCreated} onCancel={onClose} />;
}

function CreateForm({
  onBusy,
  onCreated,
  onCancel,
}: {
  onBusy: (busy: boolean) => void;
  onCreated: (c: TokenCreated) => void;
  onCancel: () => void;
}): JSX.Element {
  const queryClient = useQueryClient();
  const actionScopes = useActionScopes();
  const [name, setName] = useState("");
  const [audience, setAudience] = useState<string>(TokenAudienceAPI);
  const [scopes, setScopes] = useState<ReadonlySet<string>>(new Set([TokenScopeRead]));
  const [namespaces, setNamespaces] = useState("");
  const [days, setDays] = useState(String(TokenExpiryDefaultDays));
  const [pending, setPending] = useState(false);
  const [errors, setErrors] = useState<ReadonlyMap<string, string>>(new Map());
  const [failure, setFailure] = useState<string | null>(null);

  const actionTicked = [...scopes].some((s) => s.startsWith(TokenScopeActionsPrefix));
  const daysNum = Number(days);
  const valid =
    name.trim() !== "" &&
    scopes.size > 0 &&
    Number.isInteger(daysNum) &&
    daysNum >= 1 &&
    daysNum <= TokenExpiryMaxDays;

  function toggle(scope: string, on: boolean) {
    setScopes((prev) => {
      const next = new Set(prev);
      if (on) next.add(scope);
      else next.delete(scope);
      return next;
    });
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (pending || !valid) return;
    const req: TokenCreateRequest = {
      name: name.trim(),
      audience,
      scopes: [...scopes],
      expiresInDays: daysNum,
    };
    // Comma or whitespace separated; empty entries dropped.
    const ns = actionTicked ? namespaces.split(/[\s,]+/).filter((n) => n !== "") : [];
    if (ns.length > 0) req.actionNamespaces = ns;
    setPending(true);
    onBusy(true);
    setErrors(new Map());
    setFailure(null);
    try {
      const c = await createToken(req);
      void invalidateTokens(queryClient);
      onCreated(c);
    } catch (err) {
      const fields = fieldErrors(err);
      setErrors(fields);
      if (fields.size === 0) setFailure(errorMessage(err));
    } finally {
      setPending(false);
      onBusy(false);
    }
  }

  const describe = (field: string, base?: string) =>
    [errors.has(field) ? `token-${field}-error` : undefined, base].filter(Boolean).join(" ") ||
    undefined;

  return (
    <form onSubmit={submit} className="space-y-4" noValidate>
      <DialogHeader>
        <DialogTitle>New personal access token</DialogTitle>
        <DialogDescription>
          A token acts as you, limited to its scopes, until it expires or is revoked.
        </DialogDescription>
      </DialogHeader>

      {failure ? (
        <Alert variant="destructive">
          <AlertTitle>The token was not created</AlertTitle>
          <AlertDescription>{failure}</AlertDescription>
        </Alert>
      ) : null}

      <div className="space-y-1.5">
        <Label htmlFor="token-name" className="text-xs font-medium text-muted">
          Name
        </Label>
        <Input
          id="token-name"
          autoComplete="off"
          maxLength={TokenNameMaxLen}
          value={name}
          aria-invalid={errors.has("name") ? true : undefined}
          aria-describedby={describe("name")}
          onChange={(e) => setName(e.target.value)}
        />
        <FieldError id="token-name-error" message={errors.get("name")} />
      </div>

      <fieldset
        className="space-y-1.5"
        aria-invalid={errors.has("audience") ? true : undefined}
        aria-describedby={describe("audience")}
      >
        <legend className="mb-1 text-xs font-medium text-muted">Audience</legend>
        {AUDIENCES.map((a) => (
          <label key={a.value} className="flex cursor-pointer items-center gap-2 text-sm">
            <input
              type="radio"
              name="token-audience"
              value={a.value}
              checked={audience === a.value}
              onChange={() => setAudience(a.value)}
              className="accent-accent"
            />
            <span className="num text-fg">{a.label}</span>
            <span className="text-muted">{a.note}</span>
          </label>
        ))}
        <FieldError id="token-audience-error" message={errors.get("audience")} />
      </fieldset>

      <fieldset
        className="space-y-1.5"
        aria-invalid={errors.has("scopes") ? true : undefined}
        aria-describedby={describe("scopes")}
      >
        <legend className="mb-1 text-xs font-medium text-muted">Scopes</legend>
        <div className="max-h-48 space-y-1 overflow-y-auto rounded-lg border border-line bg-panel p-2">
          {[
            ...BASE_SCOPES,
            ...actionScopes.map((s) => ({
              value: s,
              note: `run ${s.slice(TokenScopeActionsPrefix.length)} actions`,
            })),
          ].map((s) => {
            const id = `token-scope-${s.value.replace(/[^\w-]/g, "-")}`;
            return (
              <label
                key={s.value}
                htmlFor={id}
                className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-sm hover:bg-panel-hover"
              >
                <Checkbox
                  id={id}
                  aria-label={s.value}
                  checked={scopes.has(s.value)}
                  onCheckedChange={(c) => toggle(s.value, c === true)}
                />
                <span className="num text-fg">{s.value}</span>
                <span className="text-muted">{s.note}</span>
              </label>
            );
          })}
        </div>
        <FieldError id="token-scopes-error" message={errors.get("scopes")} />
      </fieldset>

      {actionTicked ? (
        <div className="space-y-1.5">
          <Label htmlFor="token-actionNamespaces" className="text-xs font-medium text-muted">
            Action namespaces
          </Label>
          <Input
            id="token-actionNamespaces"
            autoComplete="off"
            spellCheck={false}
            className="num"
            placeholder="media, apps-*"
            value={namespaces}
            aria-invalid={errors.has("actionNamespaces") ? true : undefined}
            aria-describedby={describe("actionNamespaces", "token-actionNamespaces-note")}
            onChange={(e) => setNamespaces(e.target.value)}
          />
          <p id="token-actionNamespaces-note" className="text-xs text-faint">
            Exact names or a prefix ending in *, separated by commas. Empty: no limit beyond the
            plugin's own.
          </p>
          <FieldError id="token-actionNamespaces-error" message={errors.get("actionNamespaces")} />
        </div>
      ) : null}

      <div className="space-y-1.5">
        <Label htmlFor="token-expiresInDays" className="text-xs font-medium text-muted">
          Expires in (days)
        </Label>
        <Input
          id="token-expiresInDays"
          type="number"
          inputMode="numeric"
          min={1}
          max={TokenExpiryMaxDays}
          className="num w-28"
          value={days}
          aria-invalid={errors.has("expiresInDays") ? true : undefined}
          aria-describedby={describe("expiresInDays", "token-expiresInDays-note")}
          onChange={(e) => setDays(e.target.value)}
        />
        <p id="token-expiresInDays-note" className="text-xs text-faint">
          1 to {TokenExpiryMaxDays} days.
        </p>
        <FieldError id="token-expiresInDays-error" message={errors.get("expiresInDays")} />
      </div>

      <DialogFooter>
        <Button type="button" variant="outline" disabled={pending} onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={!valid || pending}>
          {pending && <Loader2 className="animate-spin" />}
          Create token
        </Button>
      </DialogFooter>
    </form>
  );
}

/** The new token, with a copy button and the warning that it is never shown again. */
function ShownOnce({
  created,
  onClose,
}: {
  created: TokenCreated;
  onClose: () => void;
}): JSX.Element {
  async function copy() {
    try {
      await navigator.clipboard.writeText(created.token);
      toast.success("Token copied");
    } catch {
      toast.error("Copying failed: select the token and copy it yourself");
    }
  }
  return (
    <div className="space-y-4">
      <DialogHeader>
        <DialogTitle>Token {created.info.name} created</DialogTitle>
        <DialogDescription>Copy it now and keep it somewhere safe.</DialogDescription>
      </DialogHeader>
      <Alert role="status">
        <TriangleAlert aria-hidden="true" />
        <AlertTitle>This token is shown once</AlertTitle>
        <AlertDescription>
          dash keeps only its hash: once this dialog is closed, nobody can show it again. A lost
          token can only be revoked and replaced.
        </AlertDescription>
      </Alert>
      <div className="flex gap-2">
        <Input
          readOnly
          aria-label="New token"
          className="num"
          value={created.token}
          onFocus={(e) => e.currentTarget.select()}
        />
        <Button type="button" variant="outline" onClick={() => void copy()}>
          <Copy aria-hidden="true" />
          Copy
        </Button>
      </div>
      <DialogFooter>
        <Button type="button" onClick={onClose}>
          Done
        </Button>
      </DialogFooter>
    </div>
  );
}
