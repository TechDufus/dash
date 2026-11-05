import { type QueryClient, useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import { useMe } from "@/api/queries";
import {
  type TokenCreated,
  type TokenCreateRequest,
  type TokenList,
  TokenScopeActionsPrefix,
} from "@/api/types";
import { type Owner, ownerParam } from "./owner-filter";

/** The paths of /api/v1/tokens (P7.1). */
export const tokensPaths = {
  list: "/api/v1/tokens",
  token: (id: string) => `/api/v1/tokens/${encodeURIComponent(id)}`,
};

/** Query keys: invalidating `tokensKeys.all` refetches every token list. */
export const tokensKeys = {
  all: ["tokens"] as const,
  list: (user: string | undefined) => ["tokens", "list", user ?? ""] as const,
};

/** GET /api/v1/tokens, for `owner`; never requested while `enabled` is false. */
export function useTokens(owner: Owner, enabled: boolean) {
  const user = ownerParam(owner);
  return useQuery({
    queryKey: tokensKeys.list(user),
    queryFn: () => api.get<TokenList>(tokensPaths.list, { user }),
    enabled,
  });
}

/** The actions:<plugin> scopes an administrator may put on a token: /me lists them. */
export function useActionScopes(): string[] {
  const me = useMe().data;
  if (me?.role !== "admin") return [];
  return me.scopes.filter((s) => s.startsWith(TokenScopeActionsPrefix));
}

/** POST /api/v1/tokens: 201 with the secret, which no later answer carries. */
export function createToken(req: TokenCreateRequest): Promise<TokenCreated> {
  return api.post<TokenCreated>(tokensPaths.list, req);
}

/** DELETE /api/v1/tokens/{id}: 204. */
export function revokeToken(id: string): Promise<void> {
  return api.del<void>(tokensPaths.token(id));
}

/** After a create or a revoke: every token list is stale. */
export function invalidateTokens(qc: QueryClient): Promise<void> {
  return qc.invalidateQueries({ queryKey: tokensKeys.all });
}
