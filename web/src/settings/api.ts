import { type QueryClient, useQuery } from "@tanstack/react-query";
import { ApiError, api } from "@/api/client";
import { settingsAvailable, useMe } from "@/api/queries";
import type {
  SettingsErrorDetails,
  SettingsFieldError,
  SettingsPluginView,
  SettingsView,
} from "@/api/types";

/**
 * The paths of /api/v1/settings/** (P4.7). A plugin `field` is the dotted path inside
 * plugins.<id> ("tokenSecret", "auth.bearerToken"): one segment, dots and all.
 */
export const settingsPaths = {
  view: "/api/v1/settings",
  schema: "/api/v1/settings/schema",
  general: "/api/v1/settings/general",
  export: "/api/v1/settings/export",
  audit: "/api/v1/settings/audit",
  plugin: (id: string) => `/api/v1/settings/plugins/${encodeURIComponent(id)}`,
  test: (id: string) => `/api/v1/settings/plugins/${encodeURIComponent(id)}/test`,
  secret: (id: string, field: string) =>
    `/api/v1/settings/plugins/${encodeURIComponent(id)}/secrets/${encodeURIComponent(field)}`,
  stored: (id: string, field: string) =>
    `/api/v1/settings/plugins/${encodeURIComponent(id)}/stored/${encodeURIComponent(field)}`,
};

/** Query keys: invalidating `settingsKeys.all` refetches every settings read. */
export const settingsKeys = {
  all: ["settings"] as const,
  view: ["settings", "view"] as const,
  plugin: (id: string) => ["settings", "plugin", id] as const,
  audit: (ring: string) => ["settings", "audit", ring] as const,
};

/**
 * Whether the settings API may be read: an administrator, outside demo mode (settingsAvailable).
 */
export function useSettingsAvailable(): boolean {
  return settingsAvailable(useMe().data);
}

export interface SettingsViewOptions {
  /** Refetch period in ms while mounted (the restart notice polls every 30 s). */
  refetchInterval?: number;
  refetchOnWindowFocus?: boolean;
}

/**
 * GET /api/v1/settings. It is never requested unless the settings API serves the user (an
 * administrator, not in demo mode), so a viewer who opens a settings page, and every page of the
 * demo, makes no settings request.
 */
export function useSettingsView(opts: SettingsViewOptions = {}) {
  const available = useSettingsAvailable();
  return useQuery({
    queryKey: settingsKeys.view,
    queryFn: () => api.get<SettingsView>(settingsPaths.view),
    enabled: available,
    refetchInterval: available ? opts.refetchInterval : false,
    refetchOnWindowFocus: opts.refetchOnWindowFocus,
  });
}

/** GET /api/v1/settings/plugins/{id}, under the same rule as useSettingsView. */
export function useSettingsPlugin(id: string) {
  const available = useSettingsAvailable();
  return useQuery({
    queryKey: settingsKeys.plugin(id),
    queryFn: () => api.get<SettingsPluginView>(settingsPaths.plugin(id)),
    enabled: available && id !== "",
  });
}

/** After any successful settings write: every settings read is stale (revision, restart list). */
export function invalidateSettings(qc: QueryClient): Promise<void> {
  return qc.invalidateQueries({ queryKey: settingsKeys.all });
}

/** The SettingsErrorDetails of a refused settings request, or undefined for any other error. */
export function settingsErrorDetails(e: unknown): SettingsErrorDetails | undefined {
  if (!(e instanceof ApiError) || typeof e.details !== "object" || e.details === null) {
    return undefined;
  }
  return e.details as SettingsErrorDetails;
}

/** The per-path problems of a refused write or test, by dotted path from the root. */
export function fieldErrors(e: unknown): ReadonlyMap<string, string> {
  const out = new Map<string, string>();
  for (const f of settingsErrorDetails(e)?.fields ?? ([] as SettingsFieldError[])) {
    if (!out.has(f.path)) out.set(f.path, f.message);
  }
  return out;
}
