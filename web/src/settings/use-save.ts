import { useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type {
  CheckReport,
  SettingsPatchResult,
  SettingsSecretWrite,
  SettingsTestRequest,
} from "@/api/types";
import { invalidateSettings, settingsPaths } from "./api";
import { type Changes, isK8sRef, setIn } from "./form-model";

/** The body of PUT .../secrets/{field}: a k8s: reference goes as `ref`, anything else as `value`. */
export function secretBody(typed: string): SettingsSecretWrite {
  return isK8sRef(typed) ? { ref: typed.trim() } : { value: typed };
}

/**
 * Writes a form's changes, each write with If-Match of the revision the previous one answered,
 * starting from the view's.
 * - A destination change (design D16) is one PUT plugins/{id} whose merge patch also carries every
 *   typed secret: the server needs the set secrets re-supplied in the same request.
 * - Otherwise the patch (when there is one) and then each typed secret through its own route,
 *   which is the only place a secret may be written without a destination change.
 * Answers the last write's result (undefined when there was nothing to write).
 */
export async function writeChanges(
  id: string,
  revision: number,
  changes: Changes,
): Promise<SettingsPatchResult | undefined> {
  if (changes.destChanged) {
    const patch = structuredClone(changes.patch);
    for (const [rel, typed] of Object.entries(changes.secrets)) {
      setIn(patch, rel, isK8sRef(typed) ? typed.trim() : typed);
    }
    return api.put<SettingsPatchResult>(settingsPaths.plugin(id), patch, { ifMatch: revision });
  }
  let rev = revision;
  let last: SettingsPatchResult | undefined;
  if (Object.keys(changes.patch).length > 0) {
    last = await api.put<SettingsPatchResult>(settingsPaths.plugin(id), changes.patch, {
      ifMatch: rev,
    });
    rev = last.revision;
  }
  for (const [rel, typed] of Object.entries(changes.secrets)) {
    last = await api.put<SettingsPatchResult>(settingsPaths.secret(id, rel), secretBody(typed), {
      ifMatch: rev,
    });
    rev = last.revision;
  }
  return last;
}

/**
 * Saves a form's changes. Whatever the outcome every settings read is refetched before the
 * mutation settles (a write may have landed before a later one failed, and a 412 needs the new
 * revision), so the form never shows values older than the store's.
 */
export function useSave(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ revision, changes }: { revision: number; changes: Changes }) => {
      try {
        return await writeChanges(id, revision, changes);
      } finally {
        await invalidateSettings(qc);
      }
    },
  });
}

/** The body of POST .../test: the pending patch and the typed secrets, keyed by relative field. */
export function testRequest(changes: Changes): SettingsTestRequest {
  const req: SettingsTestRequest = {};
  if (Object.keys(changes.patch).length > 0) req.patch = changes.patch;
  if (Object.keys(changes.secrets).length > 0) req.secrets = changes.secrets;
  return req;
}

/** Tests the form's pending changes against the plugin's checker; the store is untouched. */
export function useTestConnection(id: string) {
  return useMutation({
    mutationFn: (changes: Changes) =>
      api.post<CheckReport>(settingsPaths.test(id), testRequest(changes)),
  });
}

/** One field-level delete: a secret's Clear, or a shadowed field's "Remove stored value". */
export interface FieldDelete {
  what: "secret" | "stored";
  rel: string;
  revision: number;
}

export function useFieldDelete(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ what, rel, revision }: FieldDelete) => {
      const path =
        what === "secret" ? settingsPaths.secret(id, rel) : settingsPaths.stored(id, rel);
      try {
        return await api.del<SettingsPatchResult>(path, { ifMatch: revision });
      } finally {
        await invalidateSettings(qc);
      }
    },
  });
}
