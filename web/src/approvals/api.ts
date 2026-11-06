import { useQuery } from "@tanstack/react-query";
import { ApiError, api } from "@/api/client";
import {
  type ApprovalDecision,
  type ApprovalInfo,
  ErrCodeConflict,
  ErrCodeForbidden,
} from "@/api/types";

/** The paths of the approval routes of /api/v1 (P7.5). */
export const approvalPaths = {
  approval: (id: string) => `/api/v1/approvals/${encodeURIComponent(id)}`,
  decision: (id: string) => `/api/v1/approvals/${encodeURIComponent(id)}/decision`,
};

/** The SPA route an agent and a notification send the user to: also the short `next` of sign-in. */
export function approveRoute(id: string): string {
  return `/approve/${encodeURIComponent(id)}`;
}

/** The query key of one request: the page reads it, a decision and the topic replace it. */
export function approvalKey(id: string): readonly ["approvals", string] {
  return ["approvals", id];
}

/** Whether `id` has the form of an approval id (`appr_` and 26 base32 characters). */
export function approvalIdValid(id: string): boolean {
  return /^appr_[A-Z2-7]{26}$/.test(id);
}

/** GET /api/v1/approvals/{id}: the request as it is now. */
export function fetchApproval(id: string): Promise<ApprovalInfo> {
  return api.get<ApprovalInfo>(approvalPaths.approval(id));
}

/**
 * GET /api/v1/approvals/{id}, never requested while `enabled` is false. A request is decided once
 * and stays readable 10 minutes after, so the answer is not refetched on its own and a failure is
 * not retried (a 410 stays a 410); the page reads it again when a decision finds it decided
 * already, or when core.approvals drops it from the pending list.
 */
export function useApproval(id: string, enabled: boolean) {
  return useQuery({
    queryKey: approvalKey(id),
    queryFn: () => fetchApproval(id),
    enabled,
    retry: false,
    staleTime: Number.POSITIVE_INFINITY,
    gcTime: 0,
  });
}

/**
 * POST /api/v1/approvals/{id}/decision: the request as decided. An approved request comes back
 * after its action ran, with the outcome; a request dash had to deny (the agent lost a
 * permission) comes back denied with the reason.
 */
export function decideApproval(id: string, decision: ApprovalDecision): Promise<ApprovalInfo> {
  return api.post<ApprovalInfo>(approvalPaths.decision(id), decision);
}

/** Whether `err` says the request is gone: expired past its retention, unknown, or lost (410). */
export function approvalGone(err: unknown): boolean {
  return err instanceof ApiError && err.status === 410;
}

/** Whether `err` says the id is not an approval id at all (404). */
export function approvalMalformed(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404;
}

/** Whether `err` says the request is another user's (403 forbidden, not a CSRF failure). */
export function approvalForeign(err: unknown): boolean {
  return err instanceof ApiError && err.status === 403 && err.code === ErrCodeForbidden;
}

/** Whether `err` says the request was decided already, elsewhere (409 conflict). */
export function approvalDecided(err: unknown): boolean {
  return err instanceof ApiError && err.status === 409 && err.code === ErrCodeConflict;
}
