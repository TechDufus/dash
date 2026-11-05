// Fixtures of /api/v1/settings/** (P4.7, contract §4.6): the bodies the settings pages are built
// and tested against before the backend lands, typed by the generated wire types. The schemas in
// fixtures/ are the documents the API serves for proxmox and hardware: the plugin's own JSON
// Schema with its $defs and defaults, as internal/jsonschema reflects them. A destination is a
// `format: uri` leaf (its host or port) or an `x-dash-dest` one (tls.caFile, insecureSkipVerify).
import type {
  AuditEntry,
  CheckReport,
  Me,
  SettingField,
  SettingsAudit,
  SettingsErrorDetails,
  SettingsFieldError,
  SettingsPatchResult,
  SettingsPlugin,
  SettingsPluginView,
  SettingsView,
} from "@/api/types";
import { json, type Routes } from "@/test/harness";
import { settingsPaths } from "./api";
import hardwareSchema from "./fixtures/hardware.schema.json";
import proxmoxSchema from "./fixtures/proxmox.schema.json";

/** The revision every fixture is at; a write answers REVISION + 1. */
export const REVISION = 7;

/** GET /api/v1/me of alice in the proxy mode with `role`; `over` sets the rest. */
export function me(role: "admin" | "viewer", over: Partial<Me> = {}): Me {
  return {
    user: "alice",
    role,
    authMode: "proxy",
    scopes: ["read", "logs"],
    audience: "api",
    version: "test",
    capabilities: { actions: role === "admin", mcp: false, push: false, oauth: false },
    ui: { iconBaseUrl: "" },
    warnings: [],
    ...over,
  };
}

/** A field with the defaults of an unlocked, unset leaf; `over` sets the rest. */
export function field(path: string, type: string, over: Partial<SettingField> = {}): SettingField {
  return { path, type, set: false, source: "default", locked: false, ...over };
}

/** A leaf an environment variable sets: locked, with the variable as its ref. */
export function envField(
  path: string,
  type: string,
  ref: string,
  over: Partial<SettingField> = {},
): SettingField {
  return field(path, type, {
    set: true,
    source: "env",
    ref,
    locked: true,
    lockedBy: "env",
    ...over,
  });
}

const P = "plugins.proxmox.";
const STORE_FILE = "/data/ui-settings.yaml";

/** Every leaf of plugins.proxmox in an editable store: a URL and node saved from the UI, a token
 * ID from the config file, the token secret stored (encrypted) from the UI. */
export function proxmoxFields(): SettingField[] {
  return [
    field(`${P}enabled`, "boolean"),
    field(`${P}type`, "string", { value: "" }),
    field(`${P}url`, "string", {
      value: "https://pve.example.com:8006",
      set: true,
      source: "ui",
      ref: STORE_FILE,
      stored: true,
    }),
    field(`${P}node`, "string", {
      value: "pve",
      set: true,
      source: "ui",
      ref: STORE_FILE,
      stored: true,
    }),
    field(`${P}tokenID`, "string", {
      value: "dash@pve!dash",
      set: true,
      source: "file",
      ref: "/data/config.yaml",
      locked: true,
      lockedBy: "file",
    }),
    field(`${P}tokenSecret`, "string", {
      secret: true,
      set: true,
      source: "ui",
      ref: STORE_FILE,
      stored: true,
    }),
    field(`${P}tls.caFile`, "string", { value: "" }),
    field(`${P}tls.insecureSkipVerify`, "boolean", { value: false }),
    field(`${P}criticalVMs.ids`, "array", { value: [] }),
    field(`${P}criticalVMs.tags`, "array", { value: [] }),
    field(`${P}series.vmSelector`, "string", { value: 'id="qemu/{vmid}"' }),
    field(`${P}storage.source`, "string", { value: "api", enum: ["api", "prometheus"] }),
  ];
}

/** GET /api/v1/settings/plugins/proxmox. */
export function proxmoxView(over: Partial<SettingsPluginView> = {}): SettingsPluginView {
  return {
    id: "proxmox",
    schema: proxmoxSchema,
    fields: proxmoxFields(),
    revision: REVISION,
    testable: true,
    ...over,
  };
}

/** GET /api/v1/settings/plugins/hardware: hosts and machines are lists of objects (one leaf each,
 * edited as a JSON textarea). */
export function hardwareView(over: Partial<SettingsPluginView> = {}): SettingsPluginView {
  const H = "plugins.hardware.";
  return {
    id: "hardware",
    schema: hardwareSchema,
    fields: [
      field(`${H}enabled`, "boolean"),
      field(`${H}type`, "string", { value: "" }),
      field(`${H}hosts`, "array", {
        value: [{ nodename: "pve1", machine: "server1" }],
        set: true,
        source: "file",
        ref: "/data/config.yaml",
        locked: true,
        lockedBy: "file",
      }),
      field(`${H}machines`, "array", { value: [] }),
      field(`${H}zfsScrubMetricPrefix`, "string", { value: "" }),
    ],
    revision: REVISION,
    testable: false,
    ...over,
  };
}

/** One row of the settings plugin list. */
export function settingsPlugin(id: string, over: Partial<SettingsPlugin> = {}): SettingsPlugin {
  return {
    id,
    name: id,
    state: "healthy",
    message: "",
    configurable: true,
    testable: false,
    enabled: field(`plugins.${id}.enabled`, "boolean"),
    ...over,
  };
}

/** The general section: server.*, auth.*, settings.*, dataDir are ui-ineligible; ui.iconBaseURL
 * is writable and restart-marked. */
export function generalFields(): SettingField[] {
  const ineligible = { locked: true, lockedBy: "ui-ineligible" } as const;
  return [
    envField("server.publicURL", "string", "DASH_SERVER_PUBLIC_URL", {
      value: "https://dash.example.com",
      restart: true,
    }),
    field("server.listen", "string", { value: "127.0.0.1:8080", restart: true, ...ineligible }),
    envField("auth.mode", "string", "DASH_AUTH_MODE", {
      value: "proxy",
      restart: true,
      enum: ["local", "proxy", "oidc", "none"],
    }),
    field("auth.proxy.adminUsers", "array", { value: [], ...ineligible }),
    field("settings.ui", "string", {
      value: "editable",
      restart: true,
      enum: ["editable", "readonly"],
      ...ineligible,
    }),
    field("settings.secretNamespaces", "array", { value: [], restart: true, ...ineligible }),
    field("dataDir", "string", { value: "/data", restart: true, ...ineligible }),
    field("ui.iconBaseURL", "string", { value: "", restart: true }),
  ];
}

/** GET /api/v1/settings of an editable store with DASH_SECRET_KEY. */
export function settingsView(over: Partial<SettingsView> = {}): SettingsView {
  return {
    revision: REVISION,
    store: { readonly: false, dataDir: "/data", writable: true, secretKey: true },
    restartRequired: [],
    general: generalFields(),
    plugins: [
      settingsPlugin("proxmox", {
        name: "Proxmox VE",
        testable: true,
        enabled: field("plugins.proxmox.enabled", "boolean", {
          value: true,
          set: true,
          source: "ui",
          ref: STORE_FILE,
          stored: true,
        }),
      }),
      settingsPlugin("grafana", {
        name: "Grafana",
        state: "disabled",
        message: "not configured: set plugins.grafana.url",
        enabled: envField("plugins.grafana.enabled", "boolean", "DASH_PLUGINS_GRAFANA_ENABLED", {
          value: false,
        }),
      }),
      settingsPlugin("hardware", { name: "Hardware" }),
      settingsPlugin("launcher", { name: "Launcher", configurable: false }),
    ],
    ...over,
  };
}

/** GET /api/v1/settings as the GitOps install shows it (settings.ui readonly, everything from
 * env): every field locked by env, a secret set with no value, nothing stored. */
export function readonlySettingsView(): SettingsView {
  const v = settingsView({
    store: {
      readonly: true,
      reason: "config",
      dataDir: "/data",
      writable: false,
      secretKey: false,
    },
  });
  v.plugins = v.plugins.map((p) => ({
    ...p,
    enabled: envField(p.enabled.path, "boolean", `DASH_PLUGINS_${p.id.toUpperCase()}_ENABLED`, {
      value: true,
    }),
  }));
  return v;
}

/** GET /api/v1/settings/plugins/proxmox of the read-only GitOps install. */
export function readonlyProxmoxView(): SettingsPluginView {
  const env = (name: string) => `DASH_PLUGINS_PROXMOX_${name}`;
  return proxmoxView({
    fields: [
      envField(`${P}url`, "string", env("URL"), { value: "https://pve.example.com:8006" }),
      envField(`${P}tokenID`, "string", env("TOKEN_ID"), { value: "dash@pve!dash" }),
      envField(`${P}tokenSecret`, "string", env("TOKEN_SECRET"), { secret: true }),
      field(`${P}node`, "string", { value: "", locked: true, lockedBy: "readonly" }),
    ],
  });
}

/** A write's answer. */
export function patchResult(over: Partial<SettingsPatchResult> = {}): SettingsPatchResult {
  return { revision: REVISION + 1, restartRequired: [], ...over };
}

/** A passing connection check of Proxmox. */
export function checkOK(): CheckReport {
  return { checks: [{ name: "version", ok: true, latencyMs: 42 }] };
}

/** A failing check: the category and its fixed text, never the URL. */
export function checkFailed(
  category = "auth",
  message = "the credentials were refused",
): CheckReport {
  return { checks: [{ name: "version", ok: false, category, message, latencyMs: 12 }] };
}

/** A check whose new k8s: reference has no value yet (save first, then test). */
export function checkNotConfigured(): CheckReport {
  return checkFailed("not_configured", "not configured");
}

const AT = "2026-11-05T10:00:00Z";
const alice = { type: "proxy", user: "alice", surface: "ui" };

/** The main ring, newest first: a settings write (intent and outcome), a secret replace, a test
 * event and an action's pair. */
export function auditMain(over: Partial<SettingsAudit> = {}): SettingsAudit {
  const records: AuditEntry[] = [
    {
      time: "2026-11-05T10:05:00Z",
      kind: "outcome",
      id: "a5",
      action: "kubernetes.restart",
      target: { kind: "Deployment", namespace: "media", name: "app" },
      result: "ok",
      plugin: "kubernetes",
      actor: alice,
      requestId: "r5",
    },
    {
      time: "2026-11-05T10:04:59Z",
      kind: "intent",
      id: "a5",
      action: "kubernetes.restart",
      target: { kind: "Deployment", namespace: "media", name: "app" },
      plugin: "kubernetes",
      actor: alice,
      requestId: "r5",
    },
    {
      time: "2026-11-05T10:03:00Z",
      kind: "event",
      action: "settings.test",
      plugin: "proxmox",
      result: "ok",
      actor: alice,
    },
    {
      time: "2026-11-05T10:02:00Z",
      kind: "outcome",
      id: "a2",
      action: "settings.secret.replace",
      fields: ["plugins.proxmox.tokenSecret"],
      result: "ok",
      actor: alice,
    },
    {
      time: "2026-11-05T10:01:00Z",
      kind: "outcome",
      id: "a1",
      action: "settings.set",
      fields: ["plugins.proxmox.url", "plugins.proxmox.node"],
      result: "error",
      error: "plugins.proxmox.url: must be an http(s) URL",
      actor: alice,
    },
    {
      time: AT,
      kind: "intent",
      id: "a1",
      action: "settings.set",
      fields: ["plugins.proxmox.url", "plugins.proxmox.node"],
      actor: alice,
    },
  ];
  return { enabled: false, source: "memory", ring: "main", records, next: "main.494", ...over };
}

/** The unauth ring: denials only. */
export function auditUnauth(over: Partial<SettingsAudit> = {}): SettingsAudit {
  return {
    enabled: false,
    source: "memory",
    ring: "unauth",
    records: [
      {
        time: AT,
        kind: "denial",
        action: "GET /api/v1/settings",
        reason: "forbidden",
        actor: { type: "proxy", user: "dash-verify" },
        peer: "127.0.0.1",
      },
    ],
    ...over,
  };
}

/** GET /api/v1/settings/export of the editable fixture store. */
export const EXPORT_YAML =
  "plugins:\n  proxmox:\n    enabled: true\n    node: pve\n    url: https://pve.example.com:8006\n" +
  "    # plugins.proxmox.tokenSecret: secret stored in dash; supply env:NAME or file:/path here\n";

/** The error envelope of a settings route. */
export function settingsError(
  status: number,
  code: string,
  error: string,
  details?: SettingsErrorDetails,
): Response {
  return json({ error, code, requestId: "req-settings", ...(details ? { details } : {}) }, status);
}

/** 409 locked_field for a file, env or ui-ineligible path. */
export function lockedField(
  path: string,
  source: "file" | "env" | "ui-ineligible",
  ref?: string,
): Response {
  return settingsError(409, "locked_field", `${path} is locked`, {
    path,
    source,
    ...(ref ? { ref } : {}),
  });
}

/** 409 locked_field: a destination change while a secret comes from a locked layer. */
export function secretLocked(path: string): Response {
  return settingsError(409, "locked_field", "re-enter the secret: it is set outside the UI", {
    path,
    reason: "secret_locked",
  });
}

/** 409 readonly_settings. */
export function readonlySettings(): Response {
  return settingsError(409, "readonly_settings", "settings are read-only");
}

/** 409 conflict: no DASH_SECRET_KEY. */
export function noSecretKey(): Response {
  return settingsError(409, "conflict", "set DASH_SECRET_KEY to save secrets", {
    reason: "no_secret_key",
  });
}

/** 412 precondition_failed: someone saved since the page read the settings. */
export function staleRevision(): Response {
  return settingsError(412, "precondition_failed", "the settings changed; reload them");
}

/** 428 precondition_required: a write without If-Match. */
export function missingRevision(): Response {
  return settingsError(428, "precondition_required", "If-Match is required");
}

/** 422 invalid_input with per-path problems. */
export function invalidFields(fields: SettingsFieldError[]): Response {
  return settingsError(422, "invalid_input", "invalid settings", { fields });
}

/** 422 invalid_input that no field carries (an invalid file, a plugin's own validation). */
export function invalidConfiguration(fields: SettingsFieldError[] = []): Response {
  return settingsError(422, "invalid_input", "invalid configuration", {
    reason: "invalid_configuration",
    ...(fields.length ? { fields } : {}),
  });
}

/** 503 audit_unavailable: the audit log cannot be written, so nothing was. */
export function auditUnavailable(): Response {
  return settingsError(503, "audit_unavailable", "the audit log cannot be written");
}

/**
 * stubFetch routes of an editable install as alice, an administrator: the reads answer the
 * fixtures, every write answers patchResult(). `over` replaces or adds routes ("METHOD /path");
 * `"GET /api/v1/me": me("viewer")` makes alice a viewer.
 */
export function settingsRoutes(over: Routes = {}): Routes {
  return {
    "GET /api/v1/me": me("admin"),
    [`GET ${settingsPaths.view}`]: settingsView(),
    [`GET ${settingsPaths.plugin("proxmox")}`]: proxmoxView(),
    [`GET ${settingsPaths.plugin("hardware")}`]: hardwareView(),
    [`GET ${settingsPaths.audit}`]: ({ query }: { query: URLSearchParams }) =>
      query.get("ring") === "unauth" ? auditUnauth() : auditMain(),
    [`GET ${settingsPaths.export}`]: () =>
      new Response(EXPORT_YAML, { headers: { "Content-Type": "text/yaml" } }),
    [`PUT ${settingsPaths.general}`]: patchResult(),
    [`PUT ${settingsPaths.plugin("proxmox")}`]: patchResult(),
    [`PUT ${settingsPaths.secret("proxmox", "tokenSecret")}`]: patchResult(),
    [`DELETE ${settingsPaths.secret("proxmox", "tokenSecret")}`]: patchResult(),
    [`POST ${settingsPaths.test("proxmox")}`]: checkOK(),
    ...over,
  };
}
