package api

import (
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
)

// Audit actions of the settings API. A record names field paths, never values.
const (
	actionSettingsSet          = "settings.set"
	actionSettingsRemoveStored = "settings.remove-stored"
	actionSettingsTest         = "settings.test"
	actionSecretReplace        = "settings.secret.replace"
	actionSecretClear          = "settings.secret.clear"
)

// maxSecretBytes caps a secret typed into the UI.
const maxSecretBytes = 8 << 10

// The fixed texts of the settings errors. A message never repeats a value that was sent or
// stored (design D16): a field error is its path plus one of these.
const (
	msgUnknownField   = "unknown field"
	msgReference      = "env: and file: references cannot be set from the UI"
	msgUseSecrets     = "use the secrets endpoint"
	msgReenterSecret  = "re-enter this secret: the destination changed"
	msgNotUISecret    = "not a secret field that can be set from the UI"
	msgSecretTooLong  = "a secret is at most 8 KiB"
	msgSecretEmpty    = "a secret cannot be empty; clear it instead"
	msgSecretString   = "expected a string"
	msgSecretBody     = "send exactly one of value or ref"
	msgBadK8sRef      = "must look like k8s:<namespace>/<name>#<key>"
	msgK8sNamespace   = "a k8s: reference saved from the UI may name only dash's own namespace or one listed in settings.secretNamespaces"
	msgPluginsGeneral = "plugin settings are written through /api/v1/settings/plugins/{id}"
	msgInvalidConfig  = "invalid configuration"
	msgOperatorConfig = "the configuration file or environment has an invalid value; fix it first"
	msgOperatorRef    = "referenced by the operator's configuration"
	msgURLCredentials = "credentials belong in the secret fields"
)

// The audit outcome errors of a failed settings write: fixed texts, never an error's own (which
// can name a variable, a file or a value).
var (
	errSettingsSave   = errors.New("saving the settings failed")
	errSettingsReload = errors.New("the configuration did not reload; the change was reverted")
)

// settingsRoutes are the rows of /api/v1/settings/** (P4.7): admin only, for a person (a token is
// refused), documented under the Settings tag. Every handler answers 404 in demo mode, which
// has no settings store.
func (s *Server) settingsRoutes() []Route {
	id := plugin.Param{Name: "id", Doc: "The plugin, e.g. proxmox.", Required: true}
	field := plugin.Param{Name: "field", Doc: "The dotted path inside plugins.<id>, e.g. tokenSecret or auth.bearerToken, as one segment.", Required: true}
	row := func(r Route) Route {
		r.Role, r.Interactive, r.Tag = auth.RoleAdmin, true, "Settings"
		return r
	}
	return []Route{
		row(Route{
			ID: "getSettings", Method: "GET", Path: "/api/v1/settings",
			Summary:  "Every setting with its effective value, where it comes from and whether the UI may write it, and the plugin list",
			Response: model.SettingsView{}, Handler: s.getSettings,
		}),
		row(Route{
			ID: "getSettingsSchema", Method: "GET", Path: "/api/v1/settings/schema",
			Summary:  "The JSON Schema 2020-12 of the configuration (the config file and the UI layer)",
			Response: map[string]any{}, Handler: s.getSettingsSchema,
		}),
		row(Route{
			ID: "putSettingsGeneral", Method: "PUT", Path: "/api/v1/settings/general",
			Summary: "Write settings outside plugins.* (only ui.* and mcp.* may come from the UI): a JSON merge patch relative to the root; " +
				"If-Match: \"<revision>\" is required. Field errors can come in two rounds: a value of the wrong type stops the " +
				"resolution before the remaining checks run",
			Request: map[string]any{}, RequestDoc: "A JSON merge patch (RFC 7396) relative to the root of the configuration.",
			Response: model.SettingsPatchResult{}, Handler: s.putSettingsGeneral,
		}),
		row(Route{
			ID: "exportSettings", Method: "GET", Path: "/api/v1/settings/export",
			Summary: "The values the UI store holds, as a config.yaml snippet (text/yaml); a stored secret is a comment, never its value",
			Text:    "text/yaml", Handler: s.exportSettings,
		}),
		row(Route{
			ID: "getSettingsAudit", Method: "GET", Path: "/api/v1/settings/audit",
			Summary: "One page of the audit log, newest first",
			Query: []plugin.Param{
				{Name: "ring", Doc: "main (default: actions, settings changes, events) or unauth (refusals)."},
				{Name: "limit", Doc: "At most this many records: 1 to 500, default 100."},
				{Name: "before", Doc: "The next of the previous page of the same ring."},
			},
			Response: model.SettingsAudit{}, Handler: s.getSettingsAudit,
		}),
		row(Route{
			ID: "getSettingsPlugin", Method: "GET", Path: "/api/v1/settings/plugins/{id}", PathParams: []plugin.Param{id},
			Summary:  "A plugin's settings: its schema and every field with its provenance",
			Response: model.SettingsPluginView{}, Handler: s.getSettingsPlugin,
		}),
		row(Route{
			ID: "putSettingsPlugin", Method: "PUT", Path: "/api/v1/settings/plugins/{id}", PathParams: []plugin.Param{id},
			Summary: "Write a plugin's settings: a JSON merge patch relative to plugins.<id> (null resets a field to its default); " +
				"If-Match: \"<revision>\" is required. A change of where requests go needs every set secret in the same patch. " +
				"Field errors can come in two rounds: a value of the wrong type stops the resolution before the remaining checks run",
			Request: map[string]any{}, RequestDoc: "A JSON merge patch (RFC 7396) relative to plugins.<id>.",
			Response: model.SettingsPatchResult{}, Handler: s.putSettingsPlugin,
		}),
		row(Route{
			ID: "testSettingsPlugin", Method: "POST", Path: "/api/v1/settings/plugins/{id}/test", PathParams: []plugin.Param{id},
			Summary: "Test a plugin's connection with the pending changes, without saving them (10 a minute)",
			Request: model.SettingsTestRequest{}, RequestDoc: "The form's pending changes: a merge patch relative to plugins.<id> and the secrets typed into it.",
			Response: model.CheckReport{}, Handler: s.testSettingsPlugin,
		}),
		row(Route{
			ID: "putSettingsSecret", Method: "PUT", Path: "/api/v1/settings/plugins/{id}/secrets/{field}", PathParams: []plugin.Param{id, field},
			Summary: "Replace a secret: a value (stored encrypted, at most 8 KiB) or a k8s: reference; If-Match is optional",
			Request: model.SettingsSecretWrite{}, RequestDoc: "Exactly one of value (the secret) or ref (a k8s: reference).",
			Response: model.SettingsPatchResult{}, Handler: s.putSettingsSecret,
		}),
		row(Route{
			ID: "deleteSettingsSecret", Method: "DELETE", Path: "/api/v1/settings/plugins/{id}/secrets/{field}", PathParams: []plugin.Param{id, field},
			Summary:  "Clear a secret saved from the UI; If-Match is optional",
			Response: model.SettingsPatchResult{}, Handler: s.deleteSettingsSecret,
		}),
		row(Route{
			ID: "deleteSettingsStored", Method: "DELETE", Path: "/api/v1/settings/plugins/{id}/stored/{field}", PathParams: []plugin.Param{id, field},
			Summary:  "Remove the value the UI store holds for a field, also one the file or the environment overrides; If-Match is optional",
			Response: model.SettingsPatchResult{}, Handler: s.deleteSettingsStored,
		}),
	}
}

// settingsReply is a refused settings request: the error envelope with SettingsErrorDetails.
type settingsReply struct {
	status  int
	code    string
	msg     string
	details *model.SettingsErrorDetails
}

func (rep *settingsReply) write(w http.ResponseWriter) {
	if rep.details == nil {
		writeErr(w, rep.status, rep.code, rep.msg)
		return
	}
	writeErrDetails(w, rep.status, rep.code, rep.msg, rep.details)
}

func refusal(status int, code, msg string, d *model.SettingsErrorDetails) *settingsReply {
	return &settingsReply{status: status, code: code, msg: msg, details: d}
}

// fieldsReply is 422 invalid_input with per-path problems.
func fieldsReply(fields []model.SettingsFieldError) *settingsReply {
	return refusal(http.StatusUnprocessableEntity, model.ErrCodeInvalidInput, "invalid settings", &model.SettingsErrorDetails{Fields: fields})
}

func fieldReply(path, msg string) *settingsReply {
	return fieldsReply([]model.SettingsFieldError{{Path: path, Message: msg}})
}

func lockedReply(path string, p config.Provenance) *settingsReply {
	return refusal(http.StatusConflict, model.ErrCodeLockedField, "the field is set by the "+string(p.Source)+" and cannot be changed from the UI",
		&model.SettingsErrorDetails{Path: path, Source: model.SettingLock(p.Source), Ref: p.Ref})
}

func ineligibleReply(path string) *settingsReply {
	return refusal(http.StatusConflict, model.ErrCodeLockedField, "the field cannot be set from the UI",
		&model.SettingsErrorDetails{Path: path, Source: model.SettingLockIneligible})
}

func secretLockedReply(path string) *settingsReply {
	return refusal(http.StatusConflict, model.ErrCodeLockedField, "re-enter the secret: it is set outside the UI",
		&model.SettingsErrorDetails{Path: path, Reason: model.SettingsReasonSecretLocked})
}

func noSecretKeyReply() *settingsReply {
	return refusal(http.StatusConflict, model.ErrCodeConflict, "no secret key: set DASH_SECRET_KEY to save secrets from the UI",
		&model.SettingsErrorDetails{Reason: model.SettingsReasonNoSecretKey})
}

func readonlyReply() *settingsReply {
	return refusal(http.StatusConflict, model.ErrCodeReadonlySettings, "the settings are read-only", nil)
}

func auditReply() *settingsReply {
	return refusal(http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable, "audit log unavailable", nil)
}

func internalReply() *settingsReply {
	return refusal(http.StatusInternalServerError, model.ErrCodeInternal, "saving the settings failed", nil)
}

// configReply answers a configuration that does not resolve. A problem at a path the request
// supplied is a field error there; any other is the form-level reason invalid_configuration (the
// form cannot show a field it did not send). When the configuration without the request does
// not resolve either, the problem is the file's or the environment's, and the text says to fix
// that first. Call it under the lock (it resolves the store's own values); b is nil where no
// such check can run (a plugin's refusal of a test candidate). The error's own text is never
// sent: it can name a variable, a file or a value.
func (s *Server) configReply(err error, b SettingsBackend, supplied []string) *settingsReply {
	var fields []model.SettingsFieldError
	others := false
	for _, fe := range config.FieldErrors(err) {
		if slices.Contains(supplied, fe.Path) {
			fields = append(fields, model.SettingsFieldError{Path: fe.Path, Message: s.d.Secrets.Scrub(fe.Message)})
		} else {
			others = true
		}
	}
	if len(fields) > 0 && !others {
		return fieldsReply(fields)
	}
	msg := msgInvalidConfig
	if b != nil {
		if _, berr := b.Candidate(b.Stored()); berr != nil {
			msg = msgOperatorConfig
		}
	}
	return refusal(http.StatusUnprocessableEntity, model.ErrCodeInvalidInput, msg,
		&model.SettingsErrorDetails{Reason: model.SettingsReasonInvalidConfiguration, Fields: fields})
}

// operatorRefReply refuses a k8s: reference from the UI that names a Secret key a reference of
// the operator's configuration (the file or the environment, any field) also names: the UI
// could otherwise hand the operator's secret to a field, and a host, of its choosing.
func operatorRefReply(res *config.Resolved, path, raw string) *settingsReply {
	ref, err := secret.ParseK8sRef(strings.TrimSpace(raw))
	if err != nil || !config.OperatorK8sRef(res, ref) {
		return nil
	}
	return refusal(http.StatusConflict, model.ErrCodeLockedField, msgOperatorRef, &model.SettingsErrorDetails{Path: path})
}

// hasURLCredentials reports whether v, a JSON value from the UI, holds an absolute URL with
// userinfo anywhere (user or user:password@): credentials belong in the secret fields, and a
// plain field is stored and shown in clear.
func hasURLCredentials(v any) bool {
	return !reflect.DeepEqual(config.RedactURLs(v), v)
}

// settingsBackend is Deps.Settings, or a 404 in demo mode.
func (s *Server) settingsBackend(w http.ResponseWriter) (SettingsBackend, bool) {
	if s.d.Settings == nil {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "settings are not available in demo mode")
		return nil, false
	}
	return s.d.Settings, true
}

// settingsPlugin is the backend and the plugin id of the request, or a 404.
func (s *Server) settingsPlugin(w http.ResponseWriter, r *http.Request) (SettingsBackend, string, bool) {
	b, ok := s.settingsBackend(w)
	if !ok {
		return nil, "", false
	}
	id := r.PathValue("id")
	if !slices.ContainsFunc(b.Resolved().Tree.PluginSections(), func(p config.PluginSection) bool { return p.Name == id }) {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such plugin")
		return nil, "", false
	}
	return b, id, true
}

// settingsField is settingsPlugin plus the full path of the {field} segment. It is one segment:
// an escaped "/" in it (%2F) is a 404 like a bare one, which no route matches. The path of a
// leaf is the schema's own string, which the audit records of every request share.
func (s *Server) settingsField(w http.ResponseWriter, r *http.Request) (SettingsBackend, string, string, bool) {
	b, id, ok := s.settingsPlugin(w, r)
	if !ok {
		return nil, "", "", false
	}
	field := r.PathValue("field")
	if field == "" || strings.Contains(field, "/") {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such field")
		return nil, "", "", false
	}
	path := "plugins." + id + "." + field
	if l, ok := config.LeafAt(path); ok {
		path = l.Path
	}
	return b, id, path, true
}

// writable refuses a write the store or the auth mode does not allow (design D15): a read-only
// store (409), and auth.mode=none on a listener that is not loopback (403, noneRemoteRefused).
func (s *Server) writable(w http.ResponseWriter, r *http.Request, b SettingsBackend) bool {
	if b.Store().Readonly {
		readonlyReply().write(w)
		return false
	}
	return !s.noneRemoteRefused(w, r)
}

// noneRemoteRefused answers 403 to auth.mode=none on a listener that is not loopback (only with
// auth.none.allowRemote): an unauthenticated administrator plus a settings write, or a connection
// test that makes dash call an address of the caller's choosing, is a takeover.
func (s *Server) noneRemoteRefused(w http.ResponseWriter, r *http.Request) bool {
	if principalFrom(r.Context()).Kind == auth.KindNone && !config.LoopbackAddr(s.d.Cfg.Listen) {
		s.deny(r, obs.AuthForbidden)
		writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "settings cannot be changed in auth.mode=none on a listener that is not loopback")
		return true
	}
	return false
}

// ifMatch reads If-Match: "<revision>" (a bare number too). sent is false without the header;
// ok is false for one that names no revision.
func ifMatch(r *http.Request) (rev uint64, sent, ok bool) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		return 0, false, false
	}
	v = strings.TrimPrefix(v, "W/")
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		v = v[1 : len(v)-1]
	}
	n, err := strconv.ParseUint(v, 10, 64)
	return n, true, err == nil
}

// checkRevision compares If-Match with the store's revision. It runs inside WithLock, so no
// other write can move the revision between the check and the write. A missing header is 428
// when required; one that names another revision is 412.
func checkRevision(r *http.Request, b SettingsBackend, required bool) *settingsReply {
	rev, sent, ok := ifMatch(r)
	switch {
	case !sent && required:
		return refusal(http.StatusPreconditionRequired, model.ErrCodePreconditionRequired, `If-Match: "<revision>" is required`, nil)
	case !sent:
		return nil
	case !ok || rev != b.Revision():
		return refusal(http.StatusPreconditionFailed, model.ErrCodePreconditionFailed, "the settings changed since they were read; reload them", nil)
	}
	return nil
}

// settingsWrite runs fn, one write of the pipeline, under the reloader lock and answers its
// refusal, or the store's revision and the restart list as fn left them. Both are read inside
// the same lock: read after it, another writer's revision could reach this answer, and its next
// If-Match would pass over a change it never read.
func settingsWrite(w http.ResponseWriter, b SettingsBackend, fn func() *settingsReply) {
	var (
		rep *settingsReply
		res model.SettingsPatchResult
	)
	_ = b.WithLock(func() error {
		if rep = fn(); rep == nil {
			res = model.SettingsPatchResult{Revision: b.Revision(), RestartRequired: nonNil(b.RestartRequired())}
		}
		return nil
	})
	if rep != nil {
		rep.write(w)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func nonNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// locked reports the refusal of a write to path by the layer that sets it: a UI-ineligible
// path, or a value from the file or the environment.
func locked(res *config.Resolved, path string) *settingsReply {
	if l, ok := config.LeafAt(path); ok && l.Ineligible {
		return ineligibleReply(path)
	}
	if p := res.Provenance[path]; p.Source == config.SourceEnv || p.Source == config.SourceFile {
		return lockedReply(path, p)
	}
	return nil
}

// settingsIntent records the intent of a settings write; the reply is 503 when it cannot be
// recorded, and nothing may change then.
func (s *Server) settingsIntent(r *http.Request, action string, t audit.Target, fields []string) (audit.Pending, *settingsReply) {
	rec := s.auditRecord(r, action, t)
	rec.Fields, rec.Source = fields, "ui"
	p, err := s.d.Audit.Intent(rec)
	if err != nil {
		return audit.Pending{}, auditReply()
	}
	return p, nil
}
