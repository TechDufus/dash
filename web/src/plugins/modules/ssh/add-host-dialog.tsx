import { Check, Circle, LoaderCircle, Plus, TriangleAlert } from "lucide-react";
import { type FormEvent, type ReactNode, useId, useState } from "react";
import { Link } from "wouter";
import { useTopicData } from "@/api/stream";
import type { SettingsPluginView } from "@/api/types";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
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
import { formatBytes, formatNumber } from "@/lib/format";
import { cn } from "@/lib/utils";
import {
  draftSetting,
  effectiveName,
  type HostDraft,
  hostPath,
  liveHost,
  sameName,
  stateLabel,
  validateDraft,
} from "./hosts";
import type { Host, HostState } from "./model.gen";
import { ReplaceKeyDialog, SetupStep } from "./setup-steps";
import { configuredHosts, HOSTS_PATH, useHostsWrite, withHostKey, writeFailure } from "./write";

const STEPS = ["Reach the host", "Verify its key", "Authorize dash", "Discovered"] as const;

/** Where a host stands in the add flow: the index of the step it is on. */
export function stepOf(state: HostState | undefined): number {
  switch (state) {
    case "unverified":
    case "key_changed":
      return 1;
    case "unauthorized":
      return 2;
    case "up":
      return 3;
    default:
      return 0;
  }
}

function Steps({ current }: { current: number }) {
  return (
    <ol aria-label="Steps" className="grid grid-cols-2 gap-2 sm:grid-cols-4">
      {STEPS.map((label, i) => {
        const done = i < current || current === STEPS.length - 1;
        const active = i === current && !done;
        return (
          <li
            key={label}
            aria-current={i === current ? "step" : undefined}
            className={cn(
              "flex items-center gap-1.5 rounded-lg border px-2 py-1.5 text-xs",
              done && "border-ok/25 bg-ok/5 text-ok",
              active && "border-accent/40 bg-accent/10 text-fg",
              !done && !active && "border-line text-muted",
            )}
          >
            {done ? (
              <Check className="size-3.5 shrink-0" aria-hidden="true" />
            ) : (
              <Circle className="size-3.5 shrink-0" aria-hidden="true" />
            )}
            <span>{label}</span>
            <span className="sr-only">{done ? ", done" : active ? ", current" : ""}</span>
          </li>
        );
      })}
    </ol>
  );
}

/** What dash found on a host that is up, in a few lines. */
export function Discovered({ host }: { host: Host }) {
  const info = host.info;
  if (!info) {
    return <p className="text-sm text-muted">The host is up. dash is reading what runs on it…</p>;
  }
  const rows: [string, string][] = [];
  if (info.os) rows.push(["Operating system", info.os]);
  if (info.cpus > 0) {
    rows.push([
      "Processors",
      [info.cpuModel, `${formatNumber(info.cpus)} ${info.cpus === 1 ? "CPU" : "CPUs"}`]
        .filter((p) => p !== "")
        .join(" · "),
    ]);
  }
  if (host.stats && host.stats.memTotalBytes > 0) {
    rows.push(["Memory", formatBytes(host.stats.memTotalBytes)]);
  }
  if (info.disks.length > 0) {
    const total = info.disks.reduce((n, d) => n + d.sizeBytes, 0);
    rows.push([
      "Disks",
      `${info.disks.length}${total > 0 ? `, ${formatBytes(total)} in all` : ""}`,
    ]);
  }
  if (info.containers) {
    const c = info.containers;
    const running = c.items.filter((x) => x.state === "running").length;
    rows.push([
      "Containers",
      c.denied
        ? `${c.runtime}, not readable by this user`
        : `${running} of ${c.items.length} running (${c.runtime})`,
    ]);
  }
  if (info.services) {
    const failed = info.services.failed.length;
    rows.push([
      "Services",
      `${formatNumber(info.services.running)} running${failed > 0 ? `, ${failed} failed` : ""}`,
    ]);
  }
  return (
    <dl
      aria-label="Discovered"
      className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-1.5 text-sm"
    >
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-muted">{k}</dt>
          <dd className="num text-fg">{v}</dd>
        </div>
      ))}
    </dl>
  );
}

function FieldError({ id, message }: { id: string; message?: string }) {
  if (!message) return null;
  return (
    <p id={id} className="text-xs text-destructive">
      {message}
    </p>
  );
}

type DraftField = keyof HostDraft;

function DraftInput({
  field,
  label,
  hint,
  value,
  error,
  onChange,
  placeholder,
}: {
  field: DraftField;
  label: string;
  hint?: string;
  value: string;
  error?: string;
  onChange: (v: string) => void;
  placeholder?: string;
}) {
  const id = useId();
  const describedBy = [error && `${id}-error`, hint && `${id}-hint`].filter(Boolean).join(" ");
  return (
    <div className="space-y-1.5" data-field={field}>
      <Label htmlFor={id}>{label}</Label>
      <Input
        id={id}
        value={value}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        className="num"
        aria-invalid={error ? true : undefined}
        aria-describedby={describedBy || undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      <FieldError id={`${id}-error`} message={error} />
      {hint && (
        <p id={`${id}-hint`} className="text-xs text-muted">
          {hint}
        </p>
      )}
    </div>
  );
}

interface FormError {
  message: string;
  items: string[];
}

function AddForm({
  view,
  onAdded,
  onCancel,
  onBusy,
}: {
  view: SettingsPluginView;
  onAdded: (name: string) => void;
  onCancel: () => void;
  onBusy: (busy: boolean) => void;
}) {
  const { hosts } = configuredHosts(view);
  const { write, busy } = useHostsWrite(view);
  const [draft, setDraft] = useState<HostDraft>({ name: "", address: "", user: "" });
  const [errors, setErrors] = useState<ReadonlyMap<string, string>>(new Map());
  const [formError, setFormError] = useState<FormError>();

  const set = (field: DraftField) => (v: string) => setDraft((d) => ({ ...d, [field]: v }));

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setErrors(new Map());
    setFormError(undefined);
    const problems = validateDraft(draft, hosts);
    if (problems.size > 0) {
      const { hosts: list, ...fields } = Object.fromEntries(problems);
      setErrors(new Map(Object.entries(fields)));
      setFormError({ message: "Some fields need attention.", items: list ? [list] : [] });
      return;
    }
    onBusy(true);
    try {
      await write([...hosts, draftSetting(draft)]);
      onAdded(effectiveName(draft));
    } catch (err) {
      // The new host is the last entry of the list: its problems go under its fields.
      const f = writeFailure(err);
      const own = `${HOSTS_PATH}[${hosts.length}].`;
      const byField = new Map<string, string>();
      const items: string[] = [];
      for (const [path, msg] of f.fields) {
        const field = path.startsWith(own) ? path.slice(own.length) : undefined;
        if (field === "name" || field === "address" || field === "user") byField.set(field, msg);
        else items.push(`${path}: ${msg}`);
      }
      setErrors(byField);
      setFormError({ message: f.message, items });
    } finally {
      onBusy(false);
    }
  }

  return (
    <form noValidate onSubmit={submit} className="space-y-4" aria-label="Add a host">
      <DialogHeader>
        <DialogTitle>Add a host</DialogTitle>
        <DialogDescription>
          A Linux machine dash reads over SSH with its own key. dash installs nothing on it, writes
          nothing to it and never uses sudo.
        </DialogDescription>
      </DialogHeader>
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
      <DraftInput
        field="address"
        label="Address"
        hint="A host name or IP address, with :port when SSH does not listen on 22. IPv6 in brackets."
        placeholder="192.0.2.10"
        value={draft.address}
        error={errors.get("address")}
        onChange={set("address")}
      />
      <DraftInput
        field="user"
        label="User"
        hint="The account dash signs in as: best a user of its own with no privileges."
        placeholder="dash"
        value={draft.user}
        error={errors.get("user")}
        onChange={set("user")}
      />
      <DraftInput
        field="name"
        label="Name (optional)"
        hint="How dash shows the host. Empty uses the address without its port."
        value={draft.name}
        error={errors.get("name")}
        onChange={set("name")}
      />
      <DialogFooter>
        <Button type="button" variant="outline" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy}>
          <Plus aria-hidden="true" />
          Save and connect
        </Button>
      </DialogFooter>
    </form>
  );
}

function Follow({
  view,
  name,
  pluginMessage,
  onClose,
}: {
  view: SettingsPluginView;
  name: string;
  /** The SSH plugin's row message while it is in state error. */
  pluginMessage?: string;
  onClose: () => void;
}) {
  const data = useTopicData("ssh.hosts");
  const { field, hosts } = configuredHosts(view);
  const { write, busy } = useHostsWrite(view);
  const [failure, setFailure] = useState<string>();
  const [replacing, setReplacing] = useState<string>();
  const host = liveHost(data?.hosts, name);
  const setting = hosts.find((h) => sameName(effectiveName(h), name));
  const step = stepOf(host?.state);
  const up = host?.state === "up";

  async function trust(key: string) {
    setFailure(undefined);
    try {
      await write(withHostKey(hosts, name, key));
    } catch (e) {
      const f = writeFailure(e);
      setFailure([f.message, ...[...f.fields].map(([p, m]) => `${p}: ${m}`)].join(" "));
    }
  }

  let body: ReactNode;
  if (!setting) {
    body = <p className="text-sm text-muted">This host is no longer in the list.</p>;
  } else if (!host || host.state === "connecting") {
    body = pluginMessage ? (
      <p className="text-sm text-fg">
        Saved. dash connects once the SSH plugin runs: {pluginMessage}
      </p>
    ) : (
      <p className="flex items-center gap-2 text-sm text-muted" role="status">
        <LoaderCircle
          className="size-4 animate-spin motion-reduce:animate-none"
          aria-hidden="true"
        />
        Connecting to the host…
      </p>
    );
  } else if (host.state === "unreachable" || host.state === "unsupported") {
    body = (
      <div className="space-y-1 text-sm">
        <p className="flex items-center gap-2 text-warn">
          <TriangleAlert className="size-4 shrink-0" aria-hidden="true" />
          {stateLabel(host.state)}: {host.message}
        </p>
        {host.state === "unreachable" && (
          <p className="text-muted">
            dash keeps trying. Check the address and that SSH listens there; the host waits in the
            list meanwhile.
          </p>
        )}
      </div>
    );
  } else if (up) {
    body = <Discovered host={host} />;
  } else {
    body = (
      <SetupStep
        host={host}
        user={setting.user}
        identity={data?.identity}
        // The list was writable when the dialog opened; a lock since then shows the file's line.
        locked={field?.locked === true}
        busy={busy}
        onTrust={(key) => void trust(key)}
        onReplace={setReplacing}
      />
    );
  }

  return (
    <div className="space-y-4">
      <DialogHeader>
        <DialogTitle>
          Adding <span className="num">{name}</span>
        </DialogTitle>
        <DialogDescription>
          Closing this is safe: the host is saved, and each step is also in the host list.
        </DialogDescription>
      </DialogHeader>
      <Steps current={step} />
      <div aria-live="polite" className="min-h-16">
        {body}
      </div>
      {failure && (
        <Alert variant="destructive">
          <AlertDescription>{failure}</AlertDescription>
        </Alert>
      )}
      <DialogFooter>
        {up && (
          <Button asChild variant="outline">
            <Link href={hostPath(name)}>Open the host's page</Link>
          </Button>
        )}
        <Button type="button" onClick={onClose}>
          {up ? "Done" : "Close"}
        </Button>
      </DialogFooter>
      <ReplaceKeyDialog
        name={replacing === undefined ? undefined : name}
        busy={busy}
        onConfirm={() => {
          if (replacing !== undefined) void trust(replacing);
        }}
        onCancel={() => setReplacing(undefined)}
      />
    </div>
  );
}

export interface AddHostDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  view: SettingsPluginView;
  pluginMessage?: string;
}

/**
 * Add a host, then follow it on the topic through reach, verify key, authorize and discovered, so
 * one flow takes a host from nothing to up. The body mounts with the open dialog, so a new opening
 * starts with an empty form.
 */
export function AddHostDialog({ open, onOpenChange, view, pluginMessage }: AddHostDialogProps) {
  const [added, setAdded] = useState<string>();
  const [busy, setBusy] = useState(false);
  const close = () => {
    setAdded(undefined);
    onOpenChange(false);
  };
  return (
    <Dialog open={open} onOpenChange={(o) => !busy && (o ? onOpenChange(true) : close())}>
      <DialogContent className="sm:max-w-xl">
        {!open ? null : added === undefined ? (
          <AddForm view={view} onAdded={setAdded} onCancel={close} onBusy={setBusy} />
        ) : (
          <Follow view={view} name={added} pluginMessage={pluginMessage} onClose={close} />
        )}
      </DialogContent>
    </Dialog>
  );
}
