import { Ban, Bot, Check, CircleCheck, CircleX, Loader2 } from "lucide-react";
import { type JSX, type ReactNode, useEffect, useState } from "react";
import { Link } from "wouter";
import { errorMessage } from "@/api/client";
import { approvalsAvailable, useMe } from "@/api/queries";
import {
  type ApprovalDecision,
  type ApprovalInfo,
  ApprovalStateDenied,
  ApprovalStateExpired,
  ApprovalStatePending,
  ConfirmTypeName,
} from "@/api/types";
import { browserNav } from "@/auth/handle401";
import { Page } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { formatDateTime } from "@/lib/format";
import { useNow } from "@/lib/hooks";
import {
  approvalDecided,
  approvalForeign,
  approvalGone,
  approveRoute,
  decideApproval,
  fetchApproval,
  useApproval,
} from "./api";

/** What the screen says when the request is gone (expired, unknown, or lost in a restart). */
export const EXPIRED = "This request expired; ask the agent again.";
/** What the screen says for another user's request (403). */
export const FOREIGN = "This request belongs to another user.";

/** Milliseconds as `m:ss`, never below 0:00. */
export function formatCountdown(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/** A target as one line: `Deployment media/jellyfin`, or `Silence alertname` without a namespace. */
function targetWords(info: ApprovalInfo): string {
  const { kind, namespace, name } = info.target;
  const path = namespace ? `${namespace}/${name}` : name;
  return kind ? `${kind} ${path}` : path;
}

/**
 * /approve/:id (design O.3): an AI agent asked to run a plugin action as this user, and the user
 * approves or denies it here. The screen names the client and the credential it called with, the
 * action, its target and summary, and the full input that runs on approval, with a countdown to
 * the request's expiry. Signed out, the user signs in with this route as a short `next` and comes
 * back: the anonymous viewer of the local mode is sent to /login, every other mode's 401 is the
 * API client's (handle401). Where dash offers no approvals to this user (no MCP, no actions,
 * the none mode) the screen says so and reads nothing.
 */
export function ApprovePage({ id }: { id: string }): JSX.Element {
  const me = useMe();
  const anonymous = me.data?.authMode === "local" && me.data.role === "viewer";
  const enabled = !anonymous && approvalsAvailable(me.data);
  const approval = useApproval(id, enabled);

  useEffect(() => {
    if (anonymous) browserNav.assign(`/login?next=${encodeURIComponent(approveRoute(id))}`);
  }, [anonymous, id]);

  if (me.isPending || anonymous) return <Loading />;
  if (me.isError) return <Notice>Could not read who is signed in: {errorMessage(me.error)}</Notice>;
  if (!enabled) return <Notice>Agent approvals are not available on this dash.</Notice>;
  if (approval.isPending) return <Loading />;
  if (approval.isError) {
    const err = approval.error;
    if (approvalGone(err)) return <Notice>{EXPIRED}</Notice>;
    if (approvalForeign(err)) return <Notice>{FOREIGN}</Notice>;
    return <Notice>Could not read the request: {errorMessage(err)}</Notice>;
  }
  return <ApprovalView key={approval.data.id} info={approval.data} />;
}

function Loading(): JSX.Element {
  return (
    <Frame>
      <PanelSkeleton lines={6} />
    </Frame>
  );
}

/** JSON text in a scrollable block, named `label` for assistive technology. */
function JSONBlock({
  label,
  text,
  className,
}: {
  label: string;
  text: string;
  className?: string;
}) {
  return (
    <section aria-label={label} className={className}>
      <pre className="num max-h-72 overflow-auto rounded-lg border border-line bg-panel p-3 text-xs text-fg">
        {text}
      </pre>
    </section>
  );
}

function Frame({ children }: { children: ReactNode }): JSX.Element {
  return <Page className="max-w-2xl">{children}</Page>;
}

/** A state with nothing to decide, and the way back. */
function Notice({ children }: { children: ReactNode }): JSX.Element {
  return (
    <Frame>
      <Panel>
        <Empty>
          <div role="status" className="space-y-3">
            <p>{children}</p>
            <Link href="/" className="text-accent underline-offset-4 hover:underline">
              Back to Overview
            </Link>
          </div>
        </Empty>
      </Panel>
    </Frame>
  );
}

function ApprovalView({ info: loaded }: { info: ApprovalInfo }): JSX.Element {
  const [info, setInfo] = useState(loaded);
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState<"approve" | "deny" | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [gone, setGone] = useState<string | null>(null);
  const now = useNow(1000).getTime();

  const pending = info.state === ApprovalStatePending;
  const left = Date.parse(info.expiresAt) - now;
  if (gone || info.state === ApprovalStateExpired) return <Notice>{gone ?? EXPIRED}</Notice>;
  // The countdown reached zero: the server expires the request on its own, so nothing is left
  // to decide (a decision already on its way still answers).
  if (pending && left <= 0 && busy === null) return <Notice>{EXPIRED}</Notice>;

  const typeName = info.confirm === ConfirmTypeName;
  const nameMatches = !typeName || typed === info.target.name;

  async function decide(approve: boolean) {
    if (busy !== null || (approve && !nameMatches)) return;
    setBusy(approve ? "approve" : "deny");
    setFailure(null);
    const decision: ApprovalDecision =
      approve && typeName ? { approve: true, confirm: typed } : { approve };
    try {
      setInfo(await decideApproval(info.id, decision));
    } catch (err) {
      if (approvalGone(err)) setGone(EXPIRED);
      else if (approvalForeign(err)) setGone(FOREIGN);
      else if (approvalDecided(err)) {
        // Decided elsewhere (another tab, the phone): show what it became.
        try {
          setInfo(await fetchApproval(info.id));
        } catch (again) {
          setGone(approvalGone(again) ? EXPIRED : errorMessage(again));
        }
      } else setFailure(errorMessage(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Frame>
      <Panel>
        <PanelHeader title="Agent approval" icon={<Bot />}>
          {pending ? (
            <span role="timer" className="num text-xs text-muted">
              Expires in {formatCountdown(left)}
            </span>
          ) : null}
        </PanelHeader>
        <div className="space-y-4 p-4">
          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="text-lg font-semibold text-fg">
                {info.title} <span className="font-normal text-muted">· {info.plugin}</span>
              </h1>
              {info.destructive ? <Badge variant="destructive">Destructive</Badge> : null}
            </div>
            <p className="text-sm text-fg">{info.summary}</p>
          </div>

          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
            <dt className="text-muted">Client</dt>
            <dd className="text-fg">{info.clientName || "Unnamed client"}</dd>
            <dt className="text-muted">{info.grantId ? "Connected app" : "Token"}</dt>
            <dd className="text-fg">{info.credentialName}</dd>
            <dt className="text-muted">Action</dt>
            <dd className="num break-all text-fg">{info.tool}</dd>
            <dt className="text-muted">Target</dt>
            <dd className="num break-all text-fg">{targetWords(info)}</dd>
            <dt className="text-muted">Requested</dt>
            <dd className="text-fg">{formatDateTime(info.createdAt)}</dd>
          </dl>

          <div className="space-y-1">
            <p className="text-xs font-medium text-muted">Input</p>
            <JSONBlock label="Input" text={JSON.stringify(info.input, null, 2)} />
          </div>

          {pending ? (
            <Decide
              info={info}
              typeName={typeName}
              typed={typed}
              setTyped={setTyped}
              nameMatches={nameMatches}
              busy={busy}
              failure={failure}
              decide={(approve) => void decide(approve)}
            />
          ) : (
            <Decided info={info} />
          )}
        </div>
      </Panel>
    </Frame>
  );
}

function Decide({
  info,
  typeName,
  typed,
  setTyped,
  nameMatches,
  busy,
  failure,
  decide,
}: {
  info: ApprovalInfo;
  typeName: boolean;
  typed: string;
  setTyped: (v: string) => void;
  nameMatches: boolean;
  busy: "approve" | "deny" | null;
  failure: string | null;
  decide: (approve: boolean) => void;
}): JSX.Element {
  return (
    <div className="space-y-3">
      {failure ? (
        <Alert variant="destructive">
          <AlertTitle>Your answer was not sent</AlertTitle>
          <AlertDescription>{failure}</AlertDescription>
        </Alert>
      ) : null}
      {typeName ? (
        <div className="space-y-2">
          <p className="text-xs text-warn">This target is protected.</p>
          <label htmlFor="approve-confirm" className="block text-sm text-muted">
            Type <span className="num text-fg">{info.target.name}</span> to approve
          </label>
          <Input
            id="approve-confirm"
            autoComplete="off"
            spellCheck={false}
            value={typed}
            disabled={busy !== null}
            onChange={(e) => setTyped(e.target.value)}
            className="num"
          />
        </div>
      ) : null}
      <div className="flex justify-end gap-2">
        <Button variant="outline" disabled={busy !== null} onClick={() => decide(false)}>
          {busy === "deny" ? <Loader2 className="animate-spin" /> : <Ban />}
          Deny
        </Button>
        <Button
          variant={info.destructive ? "destructive" : "default"}
          disabled={busy !== null || !nameMatches}
          onClick={() => decide(true)}
        >
          {busy === "approve" ? <Loader2 className="animate-spin" /> : <Check />}
          Approve
        </Button>
      </div>
    </div>
  );
}

/** A request that was approved (with how its action ran) or denied. */
function Decided({ info }: { info: ApprovalInfo }): JSX.Element {
  if (info.state === ApprovalStateDenied) {
    return (
      <div role="status" className="flex items-start gap-2 rounded-lg border border-line p-3">
        <CircleX aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted" />
        <div className="space-y-0.5 text-sm">
          <p className="font-medium text-fg">Denied</p>
          <p className="text-muted">
            {info.reason
              ? `dash denied it: ${info.reason}`
              : `Denied by ${info.decidedBy || "you"}${info.decidedAt ? ` at ${formatDateTime(info.decidedAt)}` : ""}. The action did not run.`}
          </p>
        </div>
      </div>
    );
  }
  const by = `Approved by ${info.decidedBy || "you"}${info.decidedAt ? ` at ${formatDateTime(info.decidedAt)}` : ""}.`;
  const outcome = info.outcome;
  if (!outcome) {
    return (
      <div role="status" className="rounded-lg border border-line p-3 text-sm text-muted">
        {by} The action is running.
      </div>
    );
  }
  return (
    <div role="status" className="space-y-2 rounded-lg border border-line p-3">
      <div className="flex items-start gap-2">
        {outcome.ok ? (
          <CircleCheck aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-ok" />
        ) : (
          <CircleX aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-crit" />
        )}
        <div className="space-y-0.5 text-sm">
          <p className="font-medium text-fg">
            {outcome.ok ? "The action ran" : "The action failed"}
          </p>
          <p className="text-muted">{by}</p>
          {outcome.ok ? null : <p className="text-crit">{outcome.error}</p>}
        </div>
      </div>
      {outcome.result ? (
        <details className="text-sm">
          <summary className="cursor-pointer text-muted hover:text-fg">Result</summary>
          <JSONBlock label="Result" text={prettyJSON(outcome.result)} className="mt-2" />
          {outcome.truncated ? (
            <p className="mt-1 text-xs text-faint">The result was cut to its first 64 KiB.</p>
          ) : null}
        </details>
      ) : null}
    </div>
  );
}

/** JSON text indented, or as it came when it does not parse (a truncated result). */
function prettyJSON(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}
