import { type QueryClient, useQuery } from "@tanstack/react-query";
import { ApiError, api } from "@/api/client";
import {
  type OAuthConsent,
  type OAuthConsentDecision,
  type OAuthConsentResult,
  type OAuthGrantList,
  TokenAudienceMCP,
} from "@/api/types";
import { type Owner, ownerParam } from "@/settings/owner-filter";

/** The paths of the OAuth routes of /api/v1 (P7.2). */
export const oauthPaths = {
  consent: (id: string) => `/api/v1/oauth/consents/${encodeURIComponent(id)}`,
  grants: "/api/v1/oauth/grants",
  grant: (id: string) => `/api/v1/oauth/grants/${encodeURIComponent(id)}`,
};

/** An OAuth audience in words. */
export function audienceWords(audience: string): string {
  return audience === TokenAudienceMCP ? "agents through MCP" : "the dash API";
}

/** The SPA route /oauth/authorize sends the browser to: also the short `next` of its sign-in. */
export function consentRoute(id: string): string {
  return `/consent/${encodeURIComponent(id)}`;
}

/**
 * GET /api/v1/oauth/consents/{id}, never requested while `enabled` is false. A pending request
 * lives 10 minutes and is decided once, so the answer is never refetched and a failure is not
 * retried (a 404 stays a 404).
 */
export function useConsent(id: string, enabled: boolean) {
  return useQuery({
    queryKey: ["oauth", "consent", id],
    queryFn: () => api.get<OAuthConsent>(oauthPaths.consent(id)),
    enabled,
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: 0,
  });
}

/** POST /api/v1/oauth/consents/{id}: the address the browser goes to with the answer. */
export function decideConsent(
  id: string,
  decision: OAuthConsentDecision,
): Promise<OAuthConsentResult> {
  return api.post<OAuthConsentResult>(oauthPaths.consent(id), decision);
}

/**
 * Whether `err` says the request is gone: it expired, was decided already, or never existed (or
 * is another user's). dash answers each the same way, 404.
 */
export function consentGone(err: unknown): boolean {
  return err instanceof ApiError && (err.status === 404 || err.status === 410);
}

/** Query keys: invalidating `grantsKeys.all` refetches every grant list. */
export const grantsKeys = {
  all: ["oauth", "grants"] as const,
  list: (user: string | undefined) => ["oauth", "grants", user ?? ""] as const,
};

/** GET /api/v1/oauth/grants, for `owner`; never requested while `enabled` is false. */
export function useGrants(owner: Owner, enabled: boolean) {
  const user = ownerParam(owner);
  return useQuery({
    queryKey: grantsKeys.list(user),
    queryFn: () => api.get<OAuthGrantList>(oauthPaths.grants, { user }),
    enabled,
  });
}

/** DELETE /api/v1/oauth/grants/{id}: 204; the client's tokens stop working at once. */
export function revokeGrant(id: string): Promise<void> {
  return api.del<void>(oauthPaths.grant(id));
}

/**
 * DELETE /api/v1/oauth/grants?user=me: every grant of the caller. The proxy mode's way to
 * disconnect every app, where no "Log out everywhere" exists (the proxy owns the session).
 */
export function revokeAllMyGrants(): Promise<void> {
  return api.del<void>(`${oauthPaths.grants}?user=me`);
}

/**
 * DELETE /api/v1/oauth/grants?user=*: every grant of every user (administrators, in every auth
 * mode). The rollback step of the upgrade notes.
 */
export function revokeEveryUsersGrants(): Promise<void> {
  return api.del<void>(`${oauthPaths.grants}?user=*`);
}

/** After a revoke: every grant list is stale. */
export function invalidateGrants(qc: QueryClient): Promise<void> {
  return qc.invalidateQueries({ queryKey: grantsKeys.all });
}
