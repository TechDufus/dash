import { useQueryClient } from "@tanstack/react-query";
import { KeyRound, Loader2, Plus, Trash2 } from "lucide-react";
import { type FormEvent, type JSX, useState } from "react";
import { toast } from "sonner";
import { Link } from "wouter";
import { errorMessage } from "@/api/client";
import { useMe } from "@/api/queries";
import type { TokenInfo } from "@/api/types";
import { Page } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatDateTime } from "@/lib/format";
import { cn } from "@/lib/utils";
import { TokenCreateDialog } from "./token-create-dialog";
import {
  canManageTokens,
  invalidateTokens,
  revokeToken,
  type TokenOwner,
  useTokens,
} from "./tokens-api";

const TH = "h-9 px-3 text-xs font-medium text-muted";
const TD = "px-3 py-2 align-top";

type OwnerKind = TokenOwner["kind"];

const OWNERS: { value: OwnerKind; label: string }[] = [
  { value: "own", label: "Mine" },
  { value: "user", label: "One user" },
  { value: "all", label: "All users" },
];

/**
 * /settings/tokens: the caller's personal access tokens, a create dialog and revocation. An
 * administrator also lists one user's or everyone's tokens and may put actions:<plugin> scopes on
 * a new token. Open to every interactive user (D8); in the none mode there are no tokens.
 */
export function TokensPage(): JSX.Element {
  const me = useMe();
  const admin = me.data?.role === "admin";
  const allowed = canManageTokens(me.data);
  const [owner, setOwner] = useState<TokenOwner>({ kind: "own" });
  const [creating, setCreating] = useState(false);
  const [revoking, setRevoking] = useState<TokenInfo | null>(null);
  // A viewer only ever lists their own tokens, whatever the state says.
  const effective: TokenOwner = admin ? owner : { kind: "own" };
  const tokens = useTokens(effective, allowed);

  if (me.isPending) {
    return (
      <Page>
        <PanelSkeleton lines={4} />
      </Page>
    );
  }
  if (!allowed) {
    return (
      <Page>
        <Panel>
          <Empty>
            <div role="status" className="space-y-3">
              <p>Personal access tokens are not available: this dash has no user accounts.</p>
              <Link href="/" className="text-accent underline-offset-4 hover:underline">
                Back to Overview
              </Link>
            </div>
          </Empty>
        </Panel>
      </Page>
    );
  }

  const canCreate = me.data?.capabilities.tokens === true;
  const showUser = effective.kind !== "own";
  const now = Date.now();
  const list = tokens.data?.tokens ?? [];

  return (
    <Page>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-lg font-semibold text-fg">Tokens</h1>
        <Button size="sm" disabled={!canCreate} onClick={() => setCreating(true)}>
          <Plus aria-hidden="true" />
          New token
        </Button>
      </div>
      {!canCreate ? (
        <Alert role="status">
          <AlertTitle>Tokens cannot be created here</AlertTitle>
          <AlertDescription>
            A personal access token must outlive a restart, so dash needs a writable data directory
            (dataDir) to keep it. Existing tokens can still be revoked.
          </AlertDescription>
        </Alert>
      ) : null}
      <Panel>
        <PanelHeader title="Personal access tokens" icon={<KeyRound />}>
          {admin ? <OwnerFilter owner={owner} onChange={setOwner} /> : null}
        </PanelHeader>
        {tokens.isPending ? (
          <PanelSkeleton lines={3} className="border-0 shadow-none" />
        ) : tokens.isError ? (
          <Empty>Could not read the tokens: {errorMessage(tokens.error)}</Empty>
        ) : list.length === 0 ? (
          <Empty>No tokens yet</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="border-line hover:bg-transparent">
                <TableHead className={TH}>Name</TableHead>
                {showUser ? <TableHead className={TH}>User</TableHead> : null}
                <TableHead className={TH}>Audience</TableHead>
                <TableHead className={TH}>Scopes</TableHead>
                <TableHead className={TH}>Action namespaces</TableHead>
                <TableHead className={TH}>Created</TableHead>
                <TableHead className={TH}>Expires</TableHead>
                <TableHead className={TH}>Last used</TableHead>
                <TableHead className={TH}>
                  <span className="sr-only">Revoke</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((t) => {
                const expired = Date.parse(t.expiresAt) <= now;
                return (
                  <TableRow key={t.id} className="border-line" data-token={t.id}>
                    <TableCell className={`${TD} font-medium text-fg`}>{t.name}</TableCell>
                    {showUser ? <TableCell className={TD}>{t.user}</TableCell> : null}
                    <TableCell className={`${TD} num text-muted`}>{t.audience}</TableCell>
                    <TableCell className={`${TD} num whitespace-normal`}>
                      {t.scopes.join(", ")}
                    </TableCell>
                    <TableCell className={`${TD} num whitespace-normal text-muted`}>
                      {t.actionNamespaces?.length ? t.actionNamespaces.join(", ") : "–"}
                    </TableCell>
                    <TableCell className={`${TD} num text-muted`}>
                      {formatDateTime(t.createdAt)}
                    </TableCell>
                    <TableCell className={cn(TD, "num", expired ? "text-crit" : "text-muted")}>
                      {formatDateTime(t.expiresAt)}
                      {expired ? (
                        <Badge variant="destructive" className="ml-2">
                          expired
                        </Badge>
                      ) : null}
                    </TableCell>
                    <TableCell className={`${TD} num text-muted`}>
                      {t.lastUsedAt ? formatDateTime(t.lastUsedAt) : "never"}
                    </TableCell>
                    <TableCell className={`${TD} text-right`}>
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-label={`Revoke ${t.name}`}
                        onClick={() => setRevoking(t)}
                      >
                        <Trash2 aria-hidden="true" />
                        Revoke
                      </Button>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </Panel>
      <TokenCreateDialog open={creating && canCreate} onOpenChange={setCreating} />
      <RevokeDialog token={revoking} onClose={() => setRevoking(null)} />
    </Page>
  );
}

/** The administrator's owner filter: their own tokens, one named user's, or everyone's. */
function OwnerFilter({
  owner,
  onChange,
}: {
  owner: TokenOwner;
  onChange: (o: TokenOwner) => void;
}): JSX.Element {
  const [kind, setKind] = useState<OwnerKind>(owner.kind);
  const [name, setName] = useState(owner.kind === "user" ? owner.name : "");

  function pick(k: OwnerKind) {
    setKind(k);
    if (k === "own" || k === "all") onChange({ kind: k });
    else if (name.trim() !== "") onChange({ kind: "user", name: name.trim() });
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    if (name.trim() !== "") onChange({ kind: "user", name: name.trim() });
  }

  return (
    <div className="ml-auto flex flex-wrap items-center gap-2">
      {/* biome-ignore lint/a11y/useSemanticElements: a group of toggle buttons, not a form fieldset */}
      <div role="group" aria-label="Whose tokens" className="flex flex-wrap gap-1">
        {OWNERS.map((o) => (
          <Button
            key={o.value}
            size="sm"
            variant={o.value === kind ? "secondary" : "ghost"}
            aria-pressed={o.value === kind}
            onClick={() => pick(o.value)}
          >
            {o.label}
          </Button>
        ))}
      </div>
      {kind === "user" ? (
        <form onSubmit={submit} className="flex gap-1">
          <Input
            aria-label="User name"
            autoComplete="off"
            className="h-8 w-40"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Button type="submit" size="sm" variant="outline">
            Show
          </Button>
        </form>
      ) : null}
    </div>
  );
}

/** Asks before DELETE /api/v1/tokens/{id}; a failure keeps the dialog open with the reason. */
function RevokeDialog({
  token,
  onClose,
}: {
  token: TokenInfo | null;
  onClose: () => void;
}): JSX.Element {
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  return (
    <AlertDialog open={!!token} onOpenChange={(o) => !o && !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Revoke {token?.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            Every client using this token is refused from its next request. This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Keep</AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={busy}
            onClick={async (e) => {
              e.preventDefault();
              if (!token) return;
              setBusy(true);
              try {
                await revokeToken(token.id);
                toast.success(`Revoked ${token.name}`);
                onClose();
              } catch (err) {
                toast.error(`Revoking failed: ${errorMessage(err)}`);
              } finally {
                setBusy(false);
                void invalidateTokens(queryClient);
              }
            }}
          >
            {busy && <Loader2 className="animate-spin" />}
            Revoke
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
