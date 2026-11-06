import { useQueryClient } from "@tanstack/react-query";
import { Ban, Bot, Check, CircleCheck, CircleX, Clock, Loader2 } from "lucide-react";
import { type JSX, type ReactNode, useEffect, useRef, useState } from "react";
import { Link } from "wouter";
import { errorMessage } from "@/api/client";
import { approvalsAvailable, useMe } from "@/api/queries";
import { useTopicData } from "@/api/stream";
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
  approvalIdValid,
  approvalKey,
  approvalMalformed,
  approveRoute,
  decideApproval,
  useApproval,
} from "./api";

/** The page's title, in the document and as its heading. */
export const TITLE = "Approval";
/** What the screen says when the request is gone (past its retention, unknown, or lost in a restart). */
export const GONE = "This request is gone; ask the agent again.";
/** What an expired request says in place of a decision. */
export const EXPIRED = "This request expired before anyone decided it; the action did not run.";
/** What the screen says for another user's request (403). */
export const FOREIGN = "This request belongs to another user.";
/** What the screen says for an id that is not an approval id (malformed, or a 404). */
export const MALFORMED = "Not an approval link.";
/** What the client row says when the MCP client reported no name. */
export const CLIENT_UNREPORTED = "not reported by the client";

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
 * approves or denies it here. The screen names the credential the client called with and the
 * client's own (self-reported) name, the action, its target and summary, and the full input that
 * runs on approval, with a countdown to the request's expiry. A request decided or expired within
 * its retention shows the same details with what it became; only a request past its retention is
 * gone. The page follows core.approvals: when the request leaves the pending list (decided on
 * another device, or expired) it reads the request again. Signed out, the user signs in with this
 * route as a short `next` and comes back: the anonymous viewer of the local mode is sent to
 * /login, every other mode's 401 is the API client's (handle401). Where dash offers no approvals
 * to this user (no MCP, no actions, the none mode) the screen says so and reads nothing; an id
 * that is not an approval id is never requested.
 */
export function ApprovePage({ id }: { id: string }): JSX.Element {
  const me = useMe();
  const valid = approvalIdValid(id);
  const anonymous = me.data?.authMode === "local" && me.data.role === "viewer";
  const enabled = valid && !anonymous && approvalsAvailable(me.data);
  const approval = useApproval(id, enabled);
  useFollowPending(id, enabled);

  useEffect(() => {
    const before = document.title;
    document.title = TITLE;
    return () => {
      document.title = before;
    };
  }, []);

  useEffect(() => {
    if (valid && anonymous)
      browserNav.assign(`/login?next=${encodeURIComponent(approveRoute(id))}`);
  }, [valid, anonymous, id]);

  if (!valid) return <Notice>{MALFORMED}</Notice>;
  if (me.isPending || anonymous) return <Loading />;
  if (me.isError) return <Notice>Could not read who is signed in: {errorMessage(me.error)}</Notice>;
  if (!enabled) return <Notice>Agent approvals are not available on this dash.</Notice>;
  if (approval.isPending) return <Loading />;
  if (approval.isError) {
    const err = approval.error;
    if (approvalGone(err)) return <Notice>{GONE}</Notice>;
    if (approvalMalformed(err)) return <Notice>{MALFORMED}</Notice>;
    if (approvalForeign(err)) return <Notice>{FOREIGN}</Notice>;
    return <Notice>Could not read the request: {errorMessage(err)}</Notice>;
  }
  return <ApprovalView key={approval.data.id} info={approval.data} />;
}

/**
 * Reads the request again when core.approvals drops it from the user's pending list while the
 * page still shows it pending: it was decided elsewhere (another tab, the phone) or it expired.
 */
function useFollowPending(id: string, enabled: boolean) {
  const queryClient = useQueryClient();
  const pending = useTopicData("core.approvals")?.pending;
  const listed = pending?.some((a) => a.id === id);
  const wasListed = useRef<boolean | undefined>(undefined);
  useEffect(() => {
    const was = wasListed.current;
    wasListed.current = listed;
    if (!enabled || listed !== false || was === false) return;
    const shown = queryClient.getQueryData<ApprovalInfo>(approvalKey(id));
    if (shown?.state === ApprovalStatePending) {
      void queryClient.invalidateQueries({ queryKey: approvalKey(id) });
    }
  }, [listed, enabled, id, queryClient]);
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
        <PanelHeader title={TITLE} icon={<Bot />} />
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

/** The client as it names itself, or that it gave no name; the credential is the reliable part. */
function ClientName({ name }: { name: string }): JSX.Element {
  if (!name) return <span className="text-muted">{CLIENT_UNREPORTED}</span>;
  return (
    <>
      {name} <span className="text-muted">(as it names itself)</span>
    </>
  );
}

function ApprovalView({ info }: { info: ApprovalInfo }): JSX.Element {
  const queryClient = useQueryClient();
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState<"approve" | "deny" | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const [gone, setGone] = useState<string | null>(null);
  const now = useNow(1000).getTime();

  const left = Date.parse(info.expiresAt) - now;
  // The countdown reached zero: the server expires the request on its own, so nothing is left
  // to decide (a decision already on its way still answers).
  const pending = info.state === ApprovalStatePending && (left > 0 || busy !== null);
  if (gone) return <Notice>{gone}</Notice>;

  const typeName = info.confirm === ConfirmTypeName;
  const nameMatches = !typeName || typed === info.target.name;

  async function decide(approve: boolean) {
    if (busy !== null || (approve && !nameMatches)) return;
    setBusy(approve ? "approve" : "deny");
    setFailure(null);
    const decision: ApprovalDecision =
      approve && typeName ? { approve: true, confirm: typed } : { approve };
    try {
      queryClient.setQueryData(approvalKey(info.id), await decideApproval(info.id, decision));
    } catch (err) {
      if (approvalGone(err)) setGone(GONE);
      else if (approvalForeign(err)) setGone(FOREIGN);
      // Decided elsewhere (another tab, the phone): read what it became.
      else if (approvalDecided(err))
        await queryClient.invalidateQueries({ queryKey: approvalKey(info.id) });
      else setFailure(errorMessage(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <Frame>
      <Panel>
        <PanelHeader title={TITLE} icon={<Bot />}>
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
            <dt className="text-muted">{info.grantId ? "Connected app" : "Token"}</dt>
            <dd className="font-medium text-fg">{info.credentialName}</dd>
            <dt className="text-muted">Client</dt>
            <dd className="text-fg">
              <ClientName name={info.clientName} />
            </dd>
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
            {`Type the ${info.target.kind || "target"}'s name (${info.target.name}) to approve`}
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

/**
 * A request that was approved (with how its action ran), denied, or expired (by the server, or
 * by the countdown reaching zero before the server's word).
 */
function Decided({ info }: { info: ApprovalInfo }): JSX.Element {
  if (info.state === ApprovalStateExpired || info.state === ApprovalStatePending) {
    return (
      <div role="status" className="flex items-start gap-2 rounded-lg border border-line p-3">
        <Clock aria-hidden="true" className="mt-0.5 size-4 shrink-0 text-muted" />
        <div className="space-y-0.5 text-sm">
          <p className="font-medium text-fg">Expired</p>
          <p className="text-muted">{EXPIRED}</p>
          {info.reason ? <p className="text-muted">{info.reason}</p> : null}
        </div>
      </div>
    );
  }
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
