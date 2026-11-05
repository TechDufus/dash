import type { Health, SettingField, SettingsStore } from "@/api/types";

/** Why a field cannot be changed here, from its lockedBy and ref; undefined when it can. */
export function lockedText(f: SettingField): string | undefined {
  if (!f.locked) return undefined;
  switch (f.lockedBy) {
    case "env":
      return f.ref ? `Set by env ${f.ref}` : "Set by an environment variable";
    case "file":
      return f.ref ? `Set in the config file ${f.ref}` : "Set in the config file";
    case "ui-ineligible":
      return "Only the config file or the environment can set this";
    case "readonly":
      return "Settings are read-only";
    default:
      return "Locked";
  }
}

/** Where a field's value comes from, for the provenance column. */
export function sourceText(f: SettingField): string {
  if (f.source === "default") return "default";
  return f.ref ? `${f.source} ${f.ref}` : f.source;
}

/** The read-only banner's explanation of store.reason. */
export function readonlyReason(store: SettingsStore): string {
  switch (store.reason) {
    case "config":
      return "settings.ui is readonly: the configuration is managed outside dash.";
    case "data_dir":
      return "There is no writable data directory (DASH_DATA_DIR) to store settings in.";
    default:
      return "dash cannot store settings.";
  }
}

/** A plugin state as a health colour; the chip still shows the state's own name. */
export function stateHealth(state: string): Health {
  switch (state) {
    case "healthy":
      return "ok";
    case "degraded":
      return "warn";
    case "error":
      return "crit";
    default:
      return "unknown";
  }
}
