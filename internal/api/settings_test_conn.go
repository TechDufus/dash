package api

import (
	"errors"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/upstream"
)

// testSettingsPlugin answers POST /api/v1/settings/plugins/{id}/test: the plugin's connection
// check against the effective configuration with the form's pending patch and the secrets the
// form supplies. Nothing is stored and nothing that runs changes. A stored secret is never sent
// to a changed destination (design D16): it is left out of the candidate, so the upstream gets
// no credential; one from the file or the environment refuses the test. 10 tests a minute per
// principal; each is audited as a settings.test event naming the plugin only.
func (s *Server) testSettingsPlugin(w http.ResponseWriter, r *http.Request) {
	b, id, ok := s.settingsPlugin(w, r)
	if !ok || s.noneRemoteRefused(w, r) {
		return
	}
	if !b.Testable(id) {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "this plugin has no connection test")
		return
	}
	if ok, retry := s.testLimit.allow(principalFrom(r.Context()).User, s.now()); !ok {
		s.deny(r, obs.AuthRateLimit)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
		writeErr(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "too many connection tests; try again later")
		return
	}
	var body model.SettingsTestRequest
	if !s.decodeStrict(w, r, &body) {
		return
	}
	var (
		cfg      any
		fromUI   bool
		supplied []string
		rep      *settingsReply
	)
	// The candidate is resolved under the lock, which a check of up to 10 s must not hold.
	_ = b.WithLock(func() error {
		cfg, fromUI, supplied, rep = s.testCandidate(b, id, body)
		return nil
	})
	if rep != nil {
		rep.write(w)
		return
	}
	report, err := b.Check(r.Context(), id, cfg, fromUI)
	ev := s.auditRecord(r, actionSettingsTest, audit.Target{Kind: "plugin", Name: id})
	ev.Plugin, ev.Source = id, "ui"
	switch {
	case errors.Is(err, plugin.ErrNotCheckable):
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "this plugin has no connection test")
		return
	case err != nil:
		// The plugin refused the candidate; its text can hold values (ruling 8).
		ev.Result, ev.Error = audit.ResultError, msgInvalidConfig
		s.d.Audit.Event(ev)
		s.configReply(err, nil, supplied).write(w) // outside the lock: no baseline
		return
	}
	out := model.CheckReport{Checks: make([]model.ConnectionCheck, len(report.Checks))}
	var failed []string
	for i, c := range report.Checks {
		out.Checks[i] = model.ConnectionCheck{
			Name: c.Name, OK: c.OK, Category: c.Category, Message: s.d.Secrets.Scrub(c.Message), LatencyMs: c.LatencyMs,
		}
		if !c.OK {
			failed = append(failed, checkCategory(c.Category))
		}
	}
	if len(failed) > 0 {
		slices.Sort(failed)
		ev.Result, ev.Error = audit.ResultError, strings.Join(slices.Compact(failed), ", ")
	}
	s.d.Audit.Event(ev)
	writeJSON(w, http.StatusOK, out)
}

// checkCategory is a failed check's category for the audit record: one of the fixed set, or
// "failed" for anything else, so that no text a plugin made up reaches the log.
func checkCategory(c string) string {
	switch c {
	case upstream.CategoryUnreachable, upstream.CategoryTimeout, upstream.CategoryTLS, upstream.CategoryAuth,
		upstream.CategoryInvalidResponse, upstream.CategoryNotConfigured:
		return c
	}
	if code, ok := strings.CutPrefix(c, "http_"); ok && len(code) == 3 && strings.Trim(code, "0123456789") == "" {
		return c
	}
	return "failed"
}

// testCandidate builds the configuration a test checks: the running configuration with the
// patch (relative to plugins.<id>) and the supplied secrets, as the plugin receives it, and the
// paths the request supplied.
func (s *Server) testCandidate(b SettingsBackend, id string, body model.SettingsTestRequest) (any, bool, []string, *settingsReply) {
	section := "plugins." + id
	res, stored := b.Resolved(), b.Stored()
	p, fes := config.FlattenPatch(section, body.Patch, stored)
	var fields []model.SettingsFieldError
	for _, fe := range fes {
		fields = append(fields, model.SettingsFieldError{Path: fe.Path, Message: fe.Message})
	}
	paths := p.Paths()
	sets := make(map[string]any, len(p.Sets)+len(body.Secrets))
	for path, v := range p.Sets {
		leaf, _ := config.LeafAt(path)
		switch {
		case leaf.Secret:
			fields = append(fields, model.SettingsFieldError{Path: path, Message: "send a secret under secrets, not in the patch"})
		case config.HasReference(v):
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgReference})
		case hasURLCredentials(v):
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgURLCredentials})
		default:
			sets[path] = v
		}
	}
	// supplied are the secrets the form typed: they go into the candidate in memory only, sealed
	// with the key so the resolution reads them as it reads a stored one. One the file or the
	// environment sets cannot be replaced: the test would pair the operator's configuration with
	// whatever else the form changed. That is a 409, as on the secrets endpoint.
	var supplied []string
	for _, rel := range slices.Sorted(maps.Keys(body.Secrets)) {
		path, v := section+"."+rel, body.Secrets[rel]
		leaf, ok := config.LeafAt(path)
		if ok {
			path = leaf.Path
		}
		switch {
		case !ok || !leaf.Secret || leaf.Ineligible:
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgNotUISecret})
			continue
		case res.Provenance[path].Source == config.SourceEnv || res.Provenance[path].Source == config.SourceFile:
			return nil, false, nil, lockedReply(path, res.Provenance[path])
		case v == "":
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgSecretEmpty})
			continue
		case len(v) > maxSecretBytes:
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgSecretTooLong})
			continue
		case config.HasReference(v):
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgReference})
			continue
		}
		supplied = append(supplied, path)
		sets[path] = v // a k8s: reference stays one: a new one has no value yet (ruling 6)
	}
	paths = append(paths, supplied...)
	if len(fields) > 0 {
		return nil, false, paths, fieldsReply(fields)
	}
	for _, path := range supplied {
		v := sets[path].(string)
		if strings.HasPrefix(strings.TrimSpace(v), "k8s:") {
			if rep := operatorRefReply(res, path, v); rep != nil {
				return nil, false, paths, rep
			}
			if msg := s.k8sRefProblem(b, res, strings.TrimSpace(v)); msg != "" {
				return nil, false, paths, fieldReply(path, msg)
			}
			continue
		}
		if res.Keys == nil {
			return nil, false, paths, noSecretKeyReply()
		}
		sealed, err := res.Keys.Encrypt(path, v)
		if err != nil {
			return nil, false, paths, noSecretKeyReply()
		}
		sets[path] = sealed
	}
	cand, err := b.Candidate(config.UILayer(stored, sets, p.Deletes))
	if err != nil {
		return nil, false, paths, s.configReply(err, b, paths)
	}
	cfg, fromUI, ok := b.PluginConfig(cand, id)
	if !ok {
		return nil, false, paths, refusal(http.StatusNotFound, model.ErrCodeNotFound, "no such plugin", nil)
	}
	if config.DestinationChanged(res, cand, section) {
		for _, path := range config.SetSecrets(res, section) {
			// A locked secret refuses the test whether or not the form typed one.
			if src := res.Provenance[path].Source; src == config.SourceEnv || src == config.SourceFile {
				return nil, false, paths, secretLockedReply(path)
			}
			if slices.Contains(supplied, path) {
				continue
			}
			// A secret saved for the old destination stays out of the candidate's copy; the
			// running configuration's handle is not touched.
			if err := config.ReplaceSecretIn(cfg, path, secret.Value{}); err != nil {
				return nil, false, paths, internalReply()
			}
		}
	}
	return cfg, fromUI, paths, nil
}
