import { useQuery } from "@tanstack/react-query";
import { ApiError, api } from "@/api/client";
import type { OAuthConsent, OAuthConsentDecision, OAuthConsentResult } from "@/api/types";

/** The paths of the OAuth routes of /api/v1 (P7.2). */
export const oauthPaths = {
  consent: (id: string) => `/api/v1/oauth/consents/${encodeURIComponent(id)}`,
};

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
