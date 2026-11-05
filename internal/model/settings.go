package model

import (
	"encoding/json"
	"time"
)

// ---- settings (P4.7: /api/v1/settings/**, admin only) ----

// SettingSource is the layer an effective value came from (design B.2): the lowest is the
// default, then the UI store, the config file and the environment.
type SettingSource string

const (
	SettingSourceDefault SettingSource = "default"
	SettingSourceUI      SettingSource = "ui"
	SettingSourceFile    SettingSource = "file"
	SettingSourceEnv     SettingSource = "env"
)

// SettingLock says why a field cannot be written from the UI. A file or env value names its
// layer (with Ref), before ui-ineligible (server.*, auth.*, settings.ui,
// settings.secretNamespaces, dataDir) and readonly (the whole store is read-only).
type SettingLock string

const (
	SettingLockFile       SettingLock = "file"
	SettingLockEnv        SettingLock = "env"
	SettingLockIneligible SettingLock = "ui-ineligible"
	SettingLockReadonly   SettingLock = "readonly"
)

// SettingField is one leaf of the configuration as the settings page shows it: its effective
// value with where it came from, and whether the UI may write it. A list, or an object inside a
// list, is one leaf and locks whole (design B.2).
type SettingField struct {
	Path string `json:"path"` // dotted, from the root: plugins.proxmox.tokenSecret
	Type string `json:"type"` // string, integer, number, boolean, array or object
	// Value is the effective value. It is absent for a secret, and a URL's userinfo is redacted.
	Value  any  `json:"value,omitempty" tstype:"unknown"`
	Secret bool `json:"secret,omitempty"`
	// Set says that a value (or, for a secret, a secret) is configured, whatever its layer.
	Set    bool          `json:"set"`
	Source SettingSource `json:"source"`
	// Ref is the environment variable, the config file path, or a secret's reference (env:NAME,
	// file:/path, k8s:ns/name#key). Never a value.
	Ref      string      `json:"ref,omitempty"`
	Locked   bool        `json:"locked"`
	LockedBy SettingLock `json:"lockedBy,omitempty"`
	// Shadowed: the store holds a UI value that a file or env value hides ("overridden by env;
	// stored value unused"). DELETE .../stored/{field} removes it.
	Shadowed bool     `json:"shadowed,omitempty"`
	Stored   bool     `json:"stored,omitempty"`  // the UI store holds a value for this path
	Restart  bool     `json:"restart,omitempty"` // a change applies after a restart (x-dash-restart)
	Enum     []string `json:"enum,omitempty"`
	Doc      string   `json:"doc,omitempty"`
}

// SettingsStoreReason says why the settings store is read-only.
type SettingsStoreReason string

const (
	// SettingsStoreReasonConfig: settings.ui is readonly (GitOps-managed).
	SettingsStoreReasonConfig SettingsStoreReason = "config"
	// SettingsStoreReasonDataDir: the data directory is missing or not writable.
	SettingsStoreReasonDataDir SettingsStoreReason = "data_dir"
)

// SettingsStore describes the UI settings store.
type SettingsStore struct {
	Readonly bool                `json:"readonly"`
	Reason   SettingsStoreReason `json:"reason,omitempty"` // set when Readonly
	DataDir  string              `json:"dataDir"`
	// Writable: the data directory can be written (the audit log file is open there), whatever
	// settings.ui says.
	Writable bool `json:"writable"`
	// SecretKey: DASH_SECRET_KEY is configured, so a secret can be saved from the UI.
	SecretKey bool `json:"secretKey"`
}

// SettingsPlugin is one row of the settings page's plugin list. State and Message are the
// plugin's row of core.plugins when the settings were read; the page follows the stream.
type SettingsPlugin struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	State   PluginState `json:"state"`
	Message string      `json:"message"`
	// Configurable: the plugin has settings (a configuration schema).
	Configurable bool `json:"configurable"`
	// Testable: POST .../plugins/{id}/test exists for it (a connection check, not demo mode).
	Testable bool         `json:"testable"`
	Enabled  SettingField `json:"enabled"` // plugins.<id>.enabled
}

// SettingsView is GET /api/v1/settings.
type SettingsView struct {
	// Revision is the store's revision: the If-Match of the next write.
	Revision uint64        `json:"revision"`
	Store    SettingsStore `json:"store"`
	// RestartRequired lists, sorted, the settings changed since this process started that apply
	// only after a restart. Empty, never null.
	RestartRequired []string `json:"restartRequired"`
	// General is every leaf outside plugins.*: server.*, auth.*, settings.*, dataDir (all
	// ui-ineligible) and ui.* (writable through PUT /settings/general, restart-marked).
	General []SettingField   `json:"general"`
	Plugins []SettingsPlugin `json:"plugins"`
}

// SettingsPluginView is GET /api/v1/settings/plugins/{id}.
type SettingsPluginView struct {
	ID string `json:"id"`
	// Schema is the plugin's configuration schema: a JSON Schema 2020-12 document whose nested
	// objects are $ref into its own $defs (internal/jsonschema). writeOnly and x-dash-secret
	// mark a secret, x-dash-restart a setting that needs a restart. A destination leaf is a
	// format: uri one (its host or port) or an x-dash-dest one (tls.*): changing it while the
	// plugin has a set secret needs every set secret re-entered (design D16).
	Schema json.RawMessage `json:"schema" tstype:"{ [key: string]: unknown }"`
	// Fields are every leaf of plugins.<id>, in schema order.
	Fields   []SettingField `json:"fields"`
	Revision uint64         `json:"revision"`
	Testable bool           `json:"testable"`
}

// SettingsPatchResult answers a successful PUT /settings/general or /settings/plugins/{id}, and
// every other settings write.
type SettingsPatchResult struct {
	Revision        uint64   `json:"revision"`
	RestartRequired []string `json:"restartRequired"`
}

// SettingsSecretWrite is the body of PUT /settings/plugins/{id}/secrets/{field...}: exactly one
// of a value (at most 8 KiB, stored encrypted) or a k8s: reference.
type SettingsSecretWrite struct {
	Value string `json:"value,omitempty"`
	Ref   string `json:"ref,omitempty"` // k8s:<namespace>/<name>#<key>
}

// SettingsTestRequest is the body of POST /settings/plugins/{id}/test: the form's pending
// changes. The candidate is the effective configuration with Patch merged in (RFC 7396,
// relative to plugins.<id>) and the supplied secrets, keyed like the secret routes' field
// (relative to plugins.<id>, e.g. tokenSecret or auth.bearerToken). A stored secret is never
// sent to a changed destination (design D16).
type SettingsTestRequest struct {
	Patch   map[string]any    `json:"patch,omitempty" tstype:"{ [key: string]: unknown }"`
	Secrets map[string]string `json:"secrets,omitempty"`
}

// CheckReport answers POST /settings/plugins/{id}/test: one result per probe, in the order they
// ran (plugin.CheckReport on the wire).
type CheckReport struct {
	Checks []ConnectionCheck `json:"checks"`
}

// ConnectionCheck is one probe of a connection check. Category is empty when OK, otherwise one
// of unreachable, timeout, tls, auth, invalid_response, not_configured or http_<code>; Message is
// the category's fixed text, never an upstream URL, host or body.
type ConnectionCheck struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Category  string `json:"category,omitempty"`
	Message   string `json:"message,omitempty"`
	LatencyMs int    `json:"latencyMs"`
}

// SettingsFieldError is one per-path problem of a write or a test.
type SettingsFieldError struct {
	Path    string `json:"path"`    // dotted, from the root
	Message string `json:"message"` // fixed text: never the value that was sent
}

// SettingsReason is the details.reason of a settings error that is not about one field.
type SettingsReason string

const (
	// SettingsReasonSecretLocked: 409 locked_field, a destination change while a secret is set
	// by a locked layer (env, file, k8s: from those); Path names the secret.
	SettingsReasonSecretLocked SettingsReason = "secret_locked"
	// SettingsReasonNoSecretKey: 409 conflict, a secret write without DASH_SECRET_KEY.
	SettingsReasonNoSecretKey SettingsReason = "no_secret_key"
	// SettingsReasonInvalidConfiguration: 422 invalid_input for a failure no field carries (an
	// invalid file or env, an unreadable stored secret, an env:/file: reference, a plugin's own
	// validation); Fields lists the per-path problems that are known.
	SettingsReasonInvalidConfiguration SettingsReason = "invalid_configuration"
)

// SettingsErrorDetails is the details of a settings route's error: 409 locked_field
// {path, source, ref} (source file, env or ui-ineligible; ref absent for ui-ineligible), 409
// locked_field {path, reason: secret_locked}, 409 conflict {reason: no_secret_key}, 422
// invalid_input {fields} or {reason: invalid_configuration, fields}.
type SettingsErrorDetails struct {
	Path   string               `json:"path,omitempty"`
	Source SettingLock          `json:"source,omitempty"`
	Ref    string               `json:"ref,omitempty"`
	Reason SettingsReason       `json:"reason,omitempty"`
	Fields []SettingsFieldError `json:"fields,omitempty"`
}

// AuditKind classifies an audit record.
type AuditKind string

const (
	AuditKindIntent  AuditKind = "intent"  // precedes a change; its outcome has the same ID
	AuditKindOutcome AuditKind = "outcome" // follows a change
	AuditKindEvent   AuditKind = "event"   // a single step: login, logout, setup, settings.test
	AuditKindDenial  AuditKind = "denial"  // a refused request
)

// AuditRing is one of the two lists the audit view pages through: denials (unauth) apart from
// everything else (main), so a flood of refusals cannot push what signed-in users did out.
type AuditRing string

const (
	AuditRingMain   AuditRing = "main"
	AuditRingUnauth AuditRing = "unauth"
)

// AuditTarget is the object an action or change applies to.
type AuditTarget struct {
	Kind      string `json:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
}

// AuditActor is who acted. Surface is ui, api or mcp. TokenID names the personal access token
// (its ID) or the static token (static:<name>) a token principal used; GrantID and ClientID
// name the OAuth grant and its client instead when the token was an OAuth access token. All
// three are empty for a person.
type AuditActor struct {
	Type     string `json:"type,omitempty"`
	User     string `json:"user,omitempty"`
	Surface  string `json:"surface,omitempty"`
	TokenID  string `json:"tokenId,omitempty"`
	GrantID  string `json:"grantId,omitempty"`
	ClientID string `json:"clientId,omitempty"`
}

// AuditEntry is one audit record as the audit view shows it: field paths only, never values,
// and no hash chain (prev).
type AuditEntry struct {
	Time      time.Time    `json:"time"`
	Kind      AuditKind    `json:"kind"`
	ID        string       `json:"id,omitempty"` // shared by an intent and its outcome
	Action    string       `json:"action,omitempty"`
	Target    *AuditTarget `json:"target,omitempty"`
	Fields    []string     `json:"fields,omitempty"`
	Result    string       `json:"result,omitempty"` // ok or error
	Error     string       `json:"error,omitempty"`
	Plugin    string       `json:"plugin,omitempty"`
	Reason    string       `json:"reason,omitempty"`
	Actor     AuditActor   `json:"actor"`
	Peer      string       `json:"peer,omitempty"`
	RequestID string       `json:"requestId,omitempty"`
}

// AuditSource says where the audit view's records come from.
type AuditSource string

const (
	AuditSourceFile   AuditSource = "file"   // audit.jsonl and its rotated files
	AuditSourceMemory AuditSource = "memory" // the in-memory rings since this start (no data dir)
)

// SettingsAudit is GET /api/v1/settings/audit?ring=&limit=&before=: one page of a ring, newest
// first. Next, when set, is the before of the next (older) page; it is opaque and valid for the
// same ring only.
type SettingsAudit struct {
	Enabled bool         `json:"enabled"` // the audit log file is on
	Source  AuditSource  `json:"source"`
	Ring    AuditRing    `json:"ring"`
	Records []AuditEntry `json:"records"`
	Next    string       `json:"next,omitempty"`
}
