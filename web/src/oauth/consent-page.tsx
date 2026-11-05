import { Loader2, PlugZap, TriangleAlert } from "lucide-react";
import { type JSX, type ReactNode, useEffect, useState } from "react";
import { Link } from "wouter";
import { ApiError, errorMessage } from "@/api/client";
import { useMe } from "@/api/queries";
import {
  ErrCodeGrantLimit,
  type Me,
  OAuthClientKindMetadata,
  type OAuthConsent,
  type OAuthConsentDecision,
  OAuthScopeApprovals,
  OAuthScopePush,
  TokenAudienceMCP,
  TokenScopeActionsPrefix,
  TokenScopeLogs,
  TokenScopeRead,
} from "@/api/types";
import { browserNav } from "@/auth/handle401";
import { Page } from "@/components/page";
import { Empty, Panel, PanelHeader, PanelSkeleton } from "@/components/panel";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { audienceWords, consentGone, consentRoute, decideConsent, useConsent } from "./api";

/** What the screen says when the request is gone (expired, decided, or never there). */
const EXPIRED = "This request expired; start again from the app.";

/** What a scope lets the app do. */
function scopeNote(scope: string): string {
  if (scope === TokenScopeRead) return "See what dash shows: every page and topic.";
  if (scope === TokenScopeLogs) return "Read and tail pod logs.";
  if (scope === OAuthScopeApprovals) return "Approve or deny the actions agents ask to run.";
  if (scope === OAuthScopePush) return "Send notifications to this device.";
  if (scope.startsWith(TokenScopeActionsPrefix)) {
    return `Run ${scope.slice(TokenScopeActionsPrefix.length)} actions.`;
  }
  return "";
}

/**
 * The warning a scope carries, or undefined. Actions depend on the audience: through MCP each
 * one waits for the user's approval (design O), through the API it runs at once. The MCP text
 * shows only where dash serves MCP (`mcp`, /me's capabilities.mcp), which no release before the
 * MCP server's does.
 */
export function scopeWarning(scope: string, audience: string, mcp: boolean): string | undefined {
  if (scope === TokenScopeLogs) return "Logs can contain secrets and personal data.";
  if (scope.startsWith(TokenScopeActionsPrefix)) {
    return mcp && audience === TokenAudienceMCP
      ? "Each action waits for your approval."
      : "An action runs without asking again.";
  }
  return undefined;
}

/**
 * /consent/:id (design M.6): an app asks, through /oauth/authorize, for access to dash as the
 * signed-in user. The screen names the app, the audience and the offered scopes, ticks only
 * `read` (defaultScopes), and sends the decision with fetch; the browser then goes to the
 * answer's redirect with location.assign, never through a form POST. Signed out, the user signs
 * in with this route as a short `next` and comes back here: the anonymous viewer of the local
 * mode is sent to /login, and every other mode's 401 is the API client's (handle401).
 */
export function ConsentPage({ id }: { id: string }): JSX.Element {
  const me = useMe();
  const anonymous = me.data?.authMode === "local" && me.data.role === "viewer";
  const enabled = !anonymous && me.data?.capabilities.oauth === true;
  const consent = useConsent(id, enabled);

  useEffect(() => {
    if (anonymous) browserNav.assign(`/login?next=${encodeURIComponent(consentRoute(id))}`);
  }, [anonymous, id]);

  if (me.isPending || anonymous) {
    return (
      <ConsentFrame>
        <PanelSkeleton lines={5} />
      </ConsentFrame>
    );
  }
  if (me.isError) return <Notice>Could not read who is signed in: {errorMessage(me.error)}</Notice>;
  if (!enabled) return <Notice>Connecting apps is not enabled on this dash.</Notice>;
  if (consent.isPending) {
    return (
      <ConsentFrame>
        <PanelSkeleton lines={5} />
      </ConsentFrame>
    );
  }
  if (consent.isError) {
    return (
      <Notice>
        {consentGone(consent.error)
          ? EXPIRED
          : `Could not read the request: ${errorMessage(consent.error)}`}
      </Notice>
    );
  }
  return <ConsentForm key={consent.data.id} consent={consent.data} me={me.data} />;
}

function ConsentFrame({ children }: { children: JSX.Element }): JSX.Element {
  return <Page className="max-w-xl">{children}</Page>;
}

/** A state with nothing to decide, and the way back. */
function Notice({ children }: { children: ReactNode }): JSX.Element {
  return (
    <ConsentFrame>
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
    </ConsentFrame>
  );
}

function ConsentForm({ consent, me }: { consent: OAuthConsent; me: Me }): JSX.Element {
  const [ticked, setTicked] = useState<ReadonlySet<string>>(
    () => new Set(consent.defaultScopes.filter((s) => consent.scopes.includes(s))),
  );
  const [busy, setBusy] = useState<"approve" | "deny" | null>(null);
  const [leaving, setLeaving] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const [gone, setGone] = useState(false);

  if (gone) return <Notice>{EXPIRED}</Notice>;

  const { client } = consent;
  const pending = busy !== null || leaving;

  function toggle(scope: string, on: boolean) {
    setTicked((prev) => {
      const next = new Set(prev);
      if (on) next.add(scope);
      else next.delete(scope);
      return next;
    });
  }

  async function decide(approve: boolean) {
    if (pending || (approve && ticked.size === 0)) return;
    setBusy(approve ? "approve" : "deny");
    setFailure(null);
    // The offered order, whatever order the boxes were ticked in.
    const decision: OAuthConsentDecision = approve
      ? { approve: true, scopes: consent.scopes.filter((s) => ticked.has(s)) }
      : { approve: false };
    try {
      const { redirect } = await decideConsent(consent.id, decision);
      setLeaving(true);
      browserNav.assign(redirect);
    } catch (err) {
      if (consentGone(err)) setGone(true);
      else if (err instanceof ApiError && err.code === ErrCodeGrantLimit) setFailure(err.message);
      else setFailure(errorMessage(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <ConsentFrame>
      <Panel>
        <PanelHeader title="Connect an app" icon={<PlugZap />} />
        <div className="space-y-4 p-4">
          <div className="space-y-1">
            <h1 className="text-lg font-semibold text-fg">Connect {client.name}?</h1>
            <p className="text-sm text-muted">
              It asks for access to {audienceWords(consent.audience)} as{" "}
              <span className="text-fg">{me.user}</span>.
            </p>
          </div>

          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-sm">
            <dt className="text-muted">
              {client.kind === OAuthClientKindMetadata ? "URL" : "Client ID"}
            </dt>
            <dd className="num break-all text-fg">{client.id}</dd>
            <dt className="text-muted">Access</dt>
            <dd className="text-fg">{audienceWords(consent.audience)}</dd>
            <dt className="text-muted">Returns to</dt>
            <dd className="num break-all text-fg">{consent.redirectUri}</dd>
          </dl>

          {failure ? (
            <Alert variant="destructive">
              <AlertTitle>Your answer was not sent</AlertTitle>
              <AlertDescription>{failure}</AlertDescription>
            </Alert>
          ) : null}

          <fieldset className="space-y-1.5">
            <legend className="mb-1 text-xs font-medium text-muted">Scopes</legend>
            <div className="space-y-1 rounded-lg border border-line bg-panel p-2">
              {consent.scopes.map((scope) => {
                const id = `consent-scope-${scope.replace(/[^\w-]/g, "-")}`;
                const warning = scopeWarning(scope, consent.audience, me.capabilities.mcp);
                const noteId = `${id}-note`;
                return (
                  <div key={scope} className="space-y-0.5 rounded px-1.5 py-1 hover:bg-panel-hover">
                    <label htmlFor={id} className="flex cursor-pointer items-center gap-2 text-sm">
                      <Checkbox
                        id={id}
                        aria-label={scope}
                        aria-describedby={noteId}
                        checked={ticked.has(scope)}
                        disabled={pending}
                        onCheckedChange={(c) => toggle(scope, c === true)}
                      />
                      <span className="num text-fg">{scope}</span>
                    </label>
                    <div id={noteId} className="pl-6 text-xs">
                      <span className="text-muted">{scopeNote(scope)}</span>
                      {warning ? (
                        <span className="mt-0.5 flex items-center gap-1 text-warn">
                          <TriangleAlert aria-hidden="true" className="size-3.5 shrink-0" />
                          {warning}
                        </span>
                      ) : null}
                    </div>
                  </div>
                );
              })}
            </div>
            {ticked.size === 0 ? (
              <p className="text-xs text-faint">Tick at least one scope to approve.</p>
            ) : null}
          </fieldset>

          <p className="text-xs text-faint">
            You can disconnect the app at any time in{" "}
            <Link
              href="/settings/connected-apps"
              className="text-accent underline-offset-4 hover:underline"
            >
              Settings → Connected apps
            </Link>
            .
          </p>

          <div className="flex justify-end gap-2">
            <Button variant="outline" disabled={pending} onClick={() => void decide(false)}>
              {busy === "deny" && <Loader2 className="animate-spin" />}
              Deny
            </Button>
            <Button disabled={pending || ticked.size === 0} onClick={() => void decide(true)}>
              {busy === "approve" && <Loader2 className="animate-spin" />}
              Approve
            </Button>
          </div>
          {leaving ? (
            <p role="status" className="text-sm text-muted">
              Returning to {client.name}…
            </p>
          ) : null}
        </div>
      </Panel>
    </ConsentFrame>
  );
}
