import { KeyRound, OctagonAlert, Plus, Server, Trash2 } from "lucide-react";
import { useMemo, useState } from "react";
import { Link } from "wouter";
import { useTopicData } from "@/api/stream";
import type { SettingField, SettingsPluginView } from "@/api/types";
import { HealthBadge } from "@/components/status";
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
import { Button } from "@/components/ui/button";
import { usePluginsState } from "@/plugins/state";
import type { SettingsFormProps } from "@/plugins/types";
import type { JsonSchema } from "@/settings/form-model";
import { lockedText } from "@/settings/list-locked";
import { SchemaForm } from "@/settings/schema-form";
import { AddHostDialog } from "./add-host-dialog";
import { CopyText } from "./copy-command";
import {
  authorizeCommand,
  effectiveName,
  type HostSetting,
  hostPath,
  liveHost,
  stateLabel,
} from "./hosts";
import type { Host, Identity } from "./model.gen";
import { ReplaceKeyDialog, SetupStep } from "./setup-steps";
import {
  configuredHosts,
  HOSTS_PATH,
  useHostsWrite,
  type WriteFailure,
  withHostKey,
  withoutHost,
  writeFailure,
} from "./write";

/** The view without plugins.ssh.hosts: what the generic form edits (the switch, the interval). */
function withoutHostsLeaf(view: SettingsPluginView): SettingsPluginView {
  const schema = view.schema as JsonSchema;
  const { hosts: _, ...properties } = schema.properties ?? {};
  return {
    ...view,
    schema: { ...schema, properties },
    fields: view.fields.filter((f) => f.path !== HOSTS_PATH),
  };
}

function IdentitySection({ identity }: { identity: Identity | undefined }) {
  return (
    <section aria-labelledby="ssh-identity" className="space-y-3">
      <h2 id="ssh-identity" className="flex items-center gap-2 text-sm font-medium text-fg">
        <KeyRound className="size-4 text-accent" aria-hidden="true" />
        dash's key
      </h2>
      {identity ? (
        <>
          <p className="text-sm text-muted">
            dash signs in to every host with this key. Authorize it once per host, as the user dash
            signs in as:
          </p>
          <CopyText
            label="Run on the host as the SSH user"
            text={authorizeCommand(identity.publicKey)}
          />
          <CopyText label="Public key" text={identity.publicKey} what="Key" />
          <p className="num text-xs break-all text-muted">{identity.fingerprint}</p>
          <p className="text-xs text-muted">
            Give dash a user of its own with no privileges (no sudo, no docker group): dash only
            reads files and runs read-only commands, and the key's restrict option allows no
            forwarding and no terminal.
          </p>
        </>
      ) : (
        <p className="text-sm text-muted">
          dash shows its public key here once the SSH plugin runs.
        </p>
      )}
    </section>
  );
}

function HostRow({
  setting,
  live,
  identity,
  locked,
  busy,
  onTrust,
  onReplace,
  onRemove,
}: {
  setting: HostSetting;
  live: Host | undefined;
  identity: Identity | undefined;
  locked: boolean;
  busy: boolean;
  onTrust: (key: string) => void;
  onReplace: (key: string) => void;
  onRemove: () => void;
}) {
  const name = effectiveName(setting);
  return (
    <li className="space-y-3 rounded-xl border border-line bg-panel p-3" aria-label={name}>
      <div className="flex flex-wrap items-start gap-x-3 gap-y-1">
        <div className="min-w-0">
          <div className="truncate text-sm font-medium text-fg">
            {live ? (
              <Link href={hostPath(live.name)} className="hover:underline">
                {name}
              </Link>
            ) : (
              name
            )}
          </div>
          <div className="num truncate text-xs text-muted">
            {setting.user}@{setting.address}
          </div>
        </div>
        <div className="ml-auto flex shrink-0 items-center gap-2">
          {live ? (
            <HealthBadge health={live.health} label={stateLabel(live.state)} />
          ) : (
            <HealthBadge health="unknown" label="Not connected" />
          )}
          {!locked && (
            <Button
              type="button"
              variant="ghost"
              size="icon-xs"
              disabled={busy}
              onClick={onRemove}
              aria-label={`Remove ${name}`}
            >
              <Trash2 aria-hidden="true" />
            </Button>
          )}
        </div>
      </div>
      {live && live.state !== "up" && <p className="text-xs text-muted">{live.message}</p>}
      {live && (
        <SetupStep
          host={live}
          user={setting.user}
          identity={identity}
          locked={locked}
          busy={busy}
          onTrust={onTrust}
          onReplace={onReplace}
        />
      )}
    </li>
  );
}

function FailureAlert({ failure }: { failure: WriteFailure }) {
  return (
    <Alert variant="destructive">
      <AlertDescription>
        <p>{failure.message}</p>
        {failure.fields.size > 0 && (
          <ul className="list-disc pl-4">
            {[...failure.fields].map(([path, msg]) => (
              <li key={path}>
                {path}: {msg}
              </li>
            ))}
          </ul>
        )}
      </AlertDescription>
    </Alert>
  );
}

function HostsSection({
  view,
  field,
  hosts,
  live,
  identity,
  onAdd,
}: {
  view: SettingsPluginView;
  field: SettingField | undefined;
  hosts: HostSetting[];
  live: readonly Host[] | undefined;
  identity: Identity | undefined;
  onAdd: () => void;
}) {
  const { write, busy } = useHostsWrite(view);
  const locked = field?.locked === true;
  const [failure, setFailure] = useState<WriteFailure>();
  const [status, setStatus] = useState<string>();
  const [replacing, setReplacing] = useState<{ name: string; key: string }>();
  const [removing, setRemoving] = useState<string>();

  async function save(next: HostSetting[], done: string) {
    setFailure(undefined);
    setStatus(undefined);
    try {
      await write(next);
      setStatus(done);
    } catch (e) {
      setFailure(writeFailure(e));
    }
  }

  return (
    <section aria-labelledby="ssh-hosts" className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <h2 id="ssh-hosts" className="flex items-center gap-2 text-sm font-medium text-fg">
          <Server className="size-4 text-accent" aria-hidden="true" />
          Hosts
        </h2>
        {!locked && (
          <Button type="button" size="sm" className="ml-auto" disabled={busy} onClick={onAdd}>
            <Plus aria-hidden="true" />
            Add host
          </Button>
        )}
      </div>
      {locked && field && (
        <p className="text-xs break-all text-muted">
          {lockedText(field)}. The list is read-only here.
        </p>
      )}
      {failure && <FailureAlert failure={failure} />}
      {hosts.length === 0 ? (
        <p className="text-sm text-muted">No host yet.</p>
      ) : (
        <ul className="space-y-3">
          {hosts.map((h) => {
            const name = effectiveName(h);
            return (
              <HostRow
                key={name}
                setting={h}
                live={liveHost(live, name)}
                identity={identity}
                locked={locked}
                busy={busy}
                onTrust={(key) => void save(withHostKey(hosts, name, key), "Key trusted.")}
                onReplace={(key) => setReplacing({ name, key })}
                onRemove={() => setRemoving(name)}
              />
            );
          })}
        </ul>
      )}
      <p role="status" className="text-sm text-muted">
        {status}
      </p>
      <ReplaceKeyDialog
        name={replacing?.name}
        busy={busy}
        onConfirm={() => {
          if (replacing) {
            void save(withHostKey(hosts, replacing.name, replacing.key), "Trusted key replaced.");
          }
        }}
        onCancel={() => setReplacing(undefined)}
      />
      <AlertDialog
        open={removing !== undefined}
        onOpenChange={(o) => !o && !busy && setRemoving(undefined)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Remove <span className="num">{removing}</span>?
            </AlertDialogTitle>
            <AlertDialogDescription>
              dash stops reading the host and forgets the key it trusted. Nothing on the host
              changes: remove dash's line from the user's ~/.ssh/authorized_keys there if dash
              should no longer sign in.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>Keep</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              disabled={busy}
              onClick={() => {
                if (removing !== undefined)
                  void save(withoutHost(hosts, removing), "Host removed.");
              }}
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  );
}

/**
 * The SSH plugin's settings: dash's public key and how to authorize it, the hosts with their live
 * state and the step each needs next (trust its key, authorize dash), an Add host flow that follows
 * a new host until it is up, and the generic form for the switch and the interval. Every change of
 * the hosts writes the whole list in one merge patch with If-Match of the view's revision.
 */
export function SSHSettings({ view }: SettingsFormProps) {
  const { field, hosts } = configuredHosts(view);
  const data = useTopicData("ssh.hosts");
  const row = usePluginsState().byId.get(view.id);
  const [adding, setAdding] = useState(false);
  const scalars = useMemo(() => withoutHostsLeaf(view), [view]);
  // A plugin that could not start (no secret key, no data directory) publishes no topic. A row
  // in error while the topic is there is a running plugin whose hosts all fail, which the host
  // list already says host by host.
  const pluginMessage = row?.state === "error" && data === undefined ? row.message : undefined;
  return (
    <div className="space-y-8">
      {pluginMessage !== undefined && (
        <Alert variant="destructive">
          <OctagonAlert aria-hidden="true" />
          <AlertTitle>The SSH plugin is not running</AlertTitle>
          <AlertDescription>
            <p>{pluginMessage}</p>
            <p>Hosts can be saved here, but dash connects to none of them until this is fixed.</p>
          </AlertDescription>
        </Alert>
      )}
      <IdentitySection identity={data?.identity} />
      <HostsSection
        view={view}
        field={field}
        hosts={hosts}
        live={data?.hosts}
        identity={data?.identity}
        onAdd={() => setAdding(true)}
      />
      <section aria-labelledby="ssh-plugin" className="space-y-3">
        <h2 id="ssh-plugin" className="text-sm font-medium text-fg">
          Plugin
        </h2>
        <SchemaForm view={scalars} />
      </section>
      <AddHostDialog
        open={adding}
        onOpenChange={setAdding}
        view={view}
        pluginMessage={pluginMessage}
      />
    </div>
  );
}
