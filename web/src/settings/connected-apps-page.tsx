import { useQueryClient } from "@tanstack/react-query";
import { Loader2, PlugZap, Trash2 } from "lucide-react";
import { type JSX, useState } from "react";
import { toast } from "sonner";
import { Link } from "wouter";
import { errorMessage } from "@/api/client";
import { canManageTokens, connectedAppsAvailable, useMe } from "@/api/queries";
import type { Me, OAuthGrantInfo } from "@/api/types";
import { Page } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
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
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { formatDateTime } from "@/lib/format";
import {
  audienceWords,
  invalidateGrants,
  revokeAllMyGrants,
  revokeGrant,
  useGrants,
} from "@/oauth/api";
import { type Owner, OwnerFilter } from "./owner-filter";

const TH = "h-9 px-3 text-xs font-medium text-muted";
const TD = "px-3 py-2 align-top";

/**
 * /settings/connected-apps: the apps the caller let in through the OAuth consent screen (design
 * M.6), with revocation. An administrator also lists one user's or everyone's. Open to every
 * interactive user where the OAuth server is on (capabilities.oauth); elsewhere the page says so
 * and reads nothing. The proxy mode, which has no "Log out everywhere", gets "Revoke all my
 * connected apps" (D23).
 */
export function ConnectedAppsPage(): JSX.Element {
  const me = useMe();
  const admin = me.data?.role === "admin";
  const available = connectedAppsAvailable(me.data);
  const [owner, setOwner] = useState<Owner>({ kind: "own" });
  const [revoking, setRevoking] = useState<OAuthGrantInfo | null>(null);
  const [revokingAll, setRevokingAll] = useState(false);
  // A viewer only ever lists their own grants, whatever the state says.
  const effective: Owner = admin ? owner : { kind: "own" };
  // "One user" before a name was typed lists nobody's grants.
  const awaitingName = effective.kind === "user" && effective.name === "";
  const grants = useGrants(effective, available && !awaitingName);

  if (me.isPending) {
    return (
      <Page>
        <PanelSkeleton lines={4} />
      </Page>
    );
  }
  if (!available) {
    return (
      <Page>
        <h1 className="text-lg font-semibold text-fg">Connected apps</h1>
        <Panel>
          <Empty>
            <div role="status" className="space-y-3">
              <Unavailable me={me.data} />
            </div>
          </Empty>
        </Panel>
      </Page>
    );
  }

  const showUser = effective.kind !== "own";
  const list = grants.data?.grants ?? [];

  return (
    <Page>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-lg font-semibold text-fg">Connected apps</h1>
        {me.data?.authMode === "proxy" ? (
          <Button size="sm" variant="destructive" onClick={() => setRevokingAll(true)}>
            <Trash2 aria-hidden="true" />
            Revoke all my connected apps
          </Button>
        ) : null}
      </div>
      <Panel>
        <PanelHeader title="Apps with access to dash" icon={<PlugZap />}>
          {admin ? (
            <OwnerFilter label="Whose connected apps" owner={owner} onChange={setOwner} />
          ) : null}
        </PanelHeader>
        {awaitingName ? (
          <Empty>Type a user name, then Show.</Empty>
        ) : grants.isPending ? (
          <PanelSkeleton lines={3} className="border-0 shadow-none" />
        ) : grants.isError ? (
          <Empty>Could not read the connected apps: {errorMessage(grants.error)}</Empty>
        ) : list.length === 0 ? (
          <Empty>No connected apps</Empty>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="border-line hover:bg-transparent">
                <TableHead className={TH}>Client</TableHead>
                {showUser ? <TableHead className={TH}>User</TableHead> : null}
                <TableHead className={TH}>Audience</TableHead>
                <TableHead className={TH}>Scopes</TableHead>
                <TableHead className={TH}>Created</TableHead>
                <TableHead className={TH}>Last used</TableHead>
                <TableHead className={TH}>
                  <span className="sr-only">Revoke</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {list.map((g) => (
                <TableRow key={g.id} className="border-line" data-grant={g.id}>
                  <TableCell className={`${TD} whitespace-normal`}>
                    <span className="block font-medium text-fg">{g.client.name}</span>
                    <span className="num block break-all text-xs text-muted">{g.client.id}</span>
                  </TableCell>
                  {showUser ? <TableCell className={TD}>{g.user}</TableCell> : null}
                  <TableCell className={`${TD} text-muted`}>{audienceWords(g.audience)}</TableCell>
                  <TableCell className={`${TD} num whitespace-normal`}>
                    {g.scopes.join(", ")}
                  </TableCell>
                  <TableCell className={`${TD} num text-muted`}>
                    {formatDateTime(g.createdAt)}
                  </TableCell>
                  <TableCell className={`${TD} num text-muted`}>
                    {g.lastUsedAt ? formatDateTime(g.lastUsedAt) : "never"}
                  </TableCell>
                  <TableCell className={`${TD} text-right`}>
                    <Button
                      size="sm"
                      variant="ghost"
                      aria-label={`Revoke ${g.client.name}`}
                      onClick={() => setRevoking(g)}
                    >
                      <Trash2 aria-hidden="true" />
                      Revoke
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Panel>
      <RevokeDialog grant={revoking} onClose={() => setRevoking(null)} />
      <RevokeAllDialog open={revokingAll} onClose={() => setRevokingAll(false)} />
    </Page>
  );
}

/** Why this dash offers no connected apps to `me`. */
function Unavailable({ me }: { me: Me | undefined }): JSX.Element {
  if (me?.authMode === "local" && me.role === "viewer") {
    return (
      <>
        <p>Sign in to manage your connected apps.</p>
        <Link href="/login" className="text-accent underline-offset-4 hover:underline">
          Sign in
        </Link>
      </>
    );
  }
  return (
    <>
      <p>
        {canManageTokens(me)
          ? "Connected apps are not available here: this dash does not authorize apps (auth.oauth.enabled is off)."
          : "Connected apps are not available: this dash has no user accounts."}
      </p>
      <Link href="/" className="text-accent underline-offset-4 hover:underline">
        Back to Overview
      </Link>
    </>
  );
}

/** One revoke request behind a confirmation; a failure keeps the dialog open with the reason. */
function useRevoke(run: () => Promise<void>, done: string, onClose: () => void) {
  const queryClient = useQueryClient();
  const [busy, setBusy] = useState(false);
  async function confirm(e: { preventDefault(): void }) {
    e.preventDefault();
    setBusy(true);
    try {
      await run();
      toast.success(done);
      onClose();
    } catch (err) {
      toast.error(`Revoking failed: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
      void invalidateGrants(queryClient);
    }
  }
  return { busy, confirm };
}

/** Asks before DELETE /api/v1/oauth/grants/{id}. */
function RevokeDialog({
  grant,
  onClose,
}: {
  grant: OAuthGrantInfo | null;
  onClose: () => void;
}): JSX.Element {
  const { busy, confirm } = useRevoke(
    () => (grant ? revokeGrant(grant.id) : Promise.resolve()),
    `Revoked ${grant?.client.name ?? "the app"}`,
    onClose,
  );
  return (
    <AlertDialog open={!!grant} onOpenChange={(o) => !o && !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Revoke {grant?.client.name}?</AlertDialogTitle>
          <AlertDialogDescription>
            The app loses its access at once; to connect again it has to ask, and you to approve.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Keep</AlertDialogCancel>
          <AlertDialogAction variant="destructive" disabled={busy} onClick={confirm}>
            {busy && <Loader2 className="animate-spin" />}
            Revoke
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}

/** Asks before DELETE /api/v1/oauth/grants?user=me (the proxy mode). */
function RevokeAllDialog({ open, onClose }: { open: boolean; onClose: () => void }): JSX.Element {
  const { busy, confirm } = useRevoke(
    revokeAllMyGrants,
    "Revoked all your connected apps",
    onClose,
  );
  return (
    <AlertDialog open={open} onOpenChange={(o) => !o && !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Revoke all your connected apps?</AlertDialogTitle>
          <AlertDialogDescription>
            Every app you connected loses its access at once. Your tokens and your sign-in stay.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Keep</AlertDialogCancel>
          <AlertDialogAction variant="destructive" disabled={busy} onClick={confirm}>
            {busy && <Loader2 className="animate-spin" />}
            Revoke all
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
