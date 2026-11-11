import { KeyRound, ShieldAlert, ShieldCheck } from "lucide-react";
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
import { CopyText } from "./copy-command";
import { authorizeCommand, fingerprintCommand } from "./hosts";
import type { Host, HostKey, Identity } from "./model.gen";

/** The key a host offers, and the command that prints the fingerprint of its own key to compare. */
function OfferedKey({ offered }: { offered: HostKey }) {
  return (
    <div className="space-y-2">
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-sm">
        <dt className="text-muted">Key type</dt>
        <dd className="num text-fg">{offered.type}</dd>
        <dt className="text-muted">Fingerprint</dt>
        <dd className="num break-all text-fg">{offered.fingerprint}</dd>
      </dl>
      <CopyText
        label="Compare it with what this prints on the host"
        text={fingerprintCommand(offered.type)}
      />
    </div>
  );
}

/** The line a locked list needs in the config file to trust the offered key. */
function HostKeyForFile({ offered }: { offered: HostKey }) {
  return (
    <CopyText
      label="The host list is set outside dash: add this to the host's entry in the config file"
      text={`hostKey: "${offered.key}"`}
      what="hostKey"
    />
  );
}

export interface SetupStepProps {
  host: Host;
  /** The SSH user dash signs in as (from the settings). */
  user: string;
  identity: Identity | undefined;
  /** The host list is set in the config file or the environment. */
  locked: boolean;
  busy: boolean;
  onTrust: (key: string) => void;
  /** Asks to replace a changed key; the caller confirms. */
  onReplace: (key: string) => void;
}

/**
 * What an administrator does next for a host that is not up yet: compare and trust its key, look
 * twice at a key that changed, or authorize dash's key. Nothing for any other state.
 */
export function SetupStep({
  host,
  user,
  identity,
  locked,
  busy,
  onTrust,
  onReplace,
}: SetupStepProps) {
  const offered = host.offeredKey;
  if (host.state === "unverified" && offered) {
    return (
      <div className="space-y-3">
        <p className="text-sm text-fg">
          dash has not sent this host anything yet. Check that the key it offers is the host's own
          before trusting it.
        </p>
        <OfferedKey offered={offered} />
        {locked ? (
          <HostKeyForFile offered={offered} />
        ) : (
          <Button type="button" size="sm" disabled={busy} onClick={() => onTrust(offered.key)}>
            <ShieldCheck aria-hidden="true" />
            Trust this key <span className="sr-only">of {host.name}</span>
          </Button>
        )}
      </div>
    );
  }
  if (host.state === "key_changed" && offered) {
    return (
      <div className="space-y-3">
        <p className="flex gap-2 text-sm text-crit">
          <ShieldAlert className="mt-0.5 size-4 shrink-0" aria-hidden="true" />
          The host offers a different key from the one dash trusts, so dash refuses to connect. A
          reinstalled host looks like this, and so does another machine answering in its place.
        </p>
        <OfferedKey offered={offered} />
        {locked ? (
          <HostKeyForFile offered={offered} />
        ) : (
          <Button
            type="button"
            size="sm"
            variant="destructive"
            disabled={busy}
            onClick={() => onReplace(offered.key)}
          >
            <KeyRound aria-hidden="true" />
            Replace the trusted key… <span className="sr-only">of {host.name}</span>
          </Button>
        )}
      </div>
    );
  }
  if (host.state === "unauthorized") {
    return (
      <div className="space-y-2">
        <p className="text-sm text-fg">
          The host does not accept dash's key for this user yet. Run this on the host as{" "}
          <span className="num">{user}</span>; dash connects on its next attempt.
        </p>
        {identity ? (
          <CopyText label="Authorize dash's key" text={authorizeCommand(identity.publicKey)} />
        ) : (
          <p className="text-xs text-muted">dash shows its public key once the SSH plugin runs.</p>
        )}
      </div>
    );
  }
  return null;
}

/**
 * The second question before a changed host key replaces the trusted one: said plainly, since
 * answering yes to an impostor hands it dash's sessions.
 */
export function ReplaceKeyDialog({
  name,
  busy,
  onConfirm,
  onCancel,
}: {
  /** The host whose key would be replaced; undefined keeps the dialog closed. */
  name: string | undefined;
  busy: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <AlertDialog open={name !== undefined} onOpenChange={(o) => !o && !busy && onCancel()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>
            Replace the trusted key of <span className="num">{name}</span>?
          </AlertDialogTitle>
          <AlertDialogDescription>
            The key differs from the one dash trusts. Only replace it if you reinstalled the host or
            changed its keys yourself, and the fingerprint matches what the host prints. If you did
            neither, another machine may be answering in its place: keep the trusted key.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Keep the trusted key</AlertDialogCancel>
          <AlertDialogAction variant="destructive" disabled={busy} onClick={onConfirm}>
            Replace the key
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
