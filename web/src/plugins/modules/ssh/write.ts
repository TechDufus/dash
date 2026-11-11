// How the SSH settings write plugins.ssh.hosts: always the whole list in one merge patch (a JSON
// merge patch replaces an array), built from the settings view's value and never from the topic,
// with If-Match of the view's revision; and how a refused write reads.
import { useIsFetching } from "@tanstack/react-query";
import { ApiError, errorMessage } from "@/api/client";
import type { SettingField, SettingsPluginView } from "@/api/types";
import { fieldErrors, settingsErrorDetails, settingsKeys } from "@/settings/api";
import { useSave } from "@/settings/use-save";
import { effectiveName, type HostSetting, sameName } from "./hosts";

export const HOSTS_PATH = "plugins.ssh.hosts";

/** The hosts field of the view and the hosts it holds (never null). */
export function configuredHosts(view: SettingsPluginView): {
  field: SettingField | undefined;
  hosts: HostSetting[];
} {
  const field = view.fields.find((f) => f.path === HOSTS_PATH);
  return { field, hosts: Array.isArray(field?.value) ? (field.value as HostSetting[]) : [] };
}

/** `hosts` with the host named `name` trusting `key` (its hostKey). */
export function withHostKey(
  hosts: readonly HostSetting[],
  name: string,
  key: string,
): HostSetting[] {
  return hosts.map((h) => (sameName(effectiveName(h), name) ? { ...h, hostKey: key } : h));
}

/** `hosts` without the host named `name`. */
export function withoutHost(hosts: readonly HostSetting[], name: string): HostSetting[] {
  return hosts.filter((h) => !sameName(effectiveName(h), name));
}

/** A refused write: the message for the alert, and the per-path problems by dotted path. */
export interface WriteFailure {
  message: string;
  fields: ReadonlyMap<string, string>;
}

/**
 * What a refused write says, as the generic settings form words it: a 412 asks to check and save
 * again (the view has been read anew), a locked field names the layer that sets it, and a 422 lists
 * its problems by path.
 */
export function writeFailure(e: unknown): WriteFailure {
  const d = settingsErrorDetails(e);
  const fields = new Map(fieldErrors(e));
  let message = errorMessage(e);
  if (d?.path && d.source) {
    fields.set(d.path, `Locked: set by ${d.source}${d.ref ? ` ${d.ref}` : ""}`);
  } else if (e instanceof ApiError && e.code === "precondition_failed") {
    message =
      "The settings changed since this page read them. It now shows the current settings: check your changes and save again.";
  }
  return { message, fields };
}

/**
 * Writes plugins.ssh.hosts. `busy` is also true while the view is being read again after a write,
 * so the next write never goes out with the revision the previous one used.
 */
export function useHostsWrite(view: SettingsPluginView) {
  const save = useSave(view.id);
  const refreshing = useIsFetching({ queryKey: settingsKeys.plugin(view.id) }) > 0;
  return {
    busy: save.isPending || refreshing,
    write: (hosts: HostSetting[]) =>
      save.mutateAsync({
        revision: view.revision,
        changes: { patch: { hosts }, secrets: {}, destChanged: false },
      }),
  };
}
