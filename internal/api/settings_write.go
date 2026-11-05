package api

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// putSettingsGeneral answers PUT /api/v1/settings/general: a merge patch relative to the root.
// Only ui.* may come from the UI; the rest is refused by the server's rule, not by a missing
// route.
func (s *Server) putSettingsGeneral(w http.ResponseWriter, r *http.Request) {
	b, ok := s.settingsBackend(w)
	if !ok {
		return
	}
	s.putSettings(w, r, b, "")
}

// putSettingsPlugin answers PUT /api/v1/settings/plugins/{id}: a merge patch relative to
// plugins.<id>.
func (s *Server) putSettingsPlugin(w http.ResponseWriter, r *http.Request) {
	b, id, ok := s.settingsPlugin(w, r)
	if !ok {
		return
	}
	s.putSettings(w, r, b, "plugins."+id)
}

// putSettings is the write pipeline of design D15 for a merge patch relative to section ("" is
// the root): the store and auth-mode checks, then under the reloader lock the If-Match check,
// the classification of every leaf, the candidate resolution, the destination rule (D16), the
// audit intent, one Apply and a Reload, which the store is reverted after if it fails.
func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, b SettingsBackend, section string) {
	if !s.writable(w, r, b) {
		return
	}
	var patch map[string]any // without UseNumber: 1 and 1.0 are the same value (ruling 5)
	if !s.decodeStrict(w, r, &patch) {
		return
	}
	settingsWrite(w, b, func() *settingsReply { return s.applyPatch(r, b, section, patch) })
}

func (s *Server) applyPatch(r *http.Request, b SettingsBackend, section string, patch map[string]any) *settingsReply {
	if rep := checkRevision(r, b, true); rep != nil {
		return rep
	}
	res, stored := b.Resolved(), b.Stored()
	p, fes := config.FlattenPatch(section, patch, stored)
	var fields []model.SettingsFieldError
	for _, fe := range fes {
		fields = append(fields, model.SettingsFieldError{Path: fe.Path, Message: fe.Message})
	}
	paths := p.Paths()
	if section == "" {
		paths = slices.DeleteFunc(paths, func(path string) bool {
			if strings.HasPrefix(path, "plugins.") {
				fields = append(fields, model.SettingsFieldError{Path: path, Message: msgPluginsGeneral})
				return true
			}
			return false
		})
	}
	if len(fields) > 0 {
		return fieldsReply(fields)
	}
	if len(paths) == 0 {
		return nil // nothing to write
	}

	// Classification, leaf by leaf: a lock is a 409 and wins over the per-field problems.
	for _, path := range paths {
		if rep := locked(res, path); rep != nil {
			return rep
		}
	}
	sets := make(map[string]any, len(p.Sets))
	var secrets, plain []string // the secret leaves of the patch; those given as plaintext
	needKey := false
	for _, path := range paths {
		leaf, _ := config.LeafAt(path)
		v, set := p.Sets[path]
		if !leaf.Secret {
			switch {
			case set && config.HasReference(v):
				fields = append(fields, model.SettingsFieldError{Path: path, Message: msgReference})
			case set && hasURLCredentials(v):
				fields = append(fields, model.SettingsFieldError{Path: path, Message: msgURLCredentials})
			}
			if set {
				sets[path] = v
			}
			continue
		}
		secrets = append(secrets, path)
		if !set {
			continue
		}
		sv, isString := v.(string)
		switch {
		case !isString:
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgSecretString})
		case config.HasReference(sv):
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgReference})
		case strings.HasPrefix(strings.TrimSpace(sv), "k8s:"):
			if rep := operatorRefReply(res, path, sv); rep != nil {
				return rep
			}
			if msg := s.k8sRefProblem(b, res, strings.TrimSpace(sv)); msg != "" {
				fields = append(fields, model.SettingsFieldError{Path: path, Message: msg})
			}
			sets[path] = strings.TrimSpace(sv)
		case sv == "":
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgSecretEmpty})
		case len(sv) > maxSecretBytes:
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgSecretTooLong})
		case res.Keys == nil:
			needKey = true
		default:
			sealed, err := res.Keys.Encrypt(path, sv)
			if err != nil {
				needKey = true
				continue
			}
			sets[path], plain = sealed, append(plain, sv)
		}
	}
	if len(fields) > 0 {
		return fieldsReply(fields)
	}

	if needKey {
		return noSecretKeyReply() // a typed secret has nowhere to be kept, whatever else the patch does
	}
	cand, err := b.Candidate(config.UILayer(stored, sets, p.Deletes))
	if err != nil {
		return s.configReply(err, b, paths)
	}

	// The destination rule (D16): a stored secret is never sent somewhere else than where it was
	// saved for. A secret may ride in a patch that edits where the section's requests go: a url
	// or dest leaf whose value changes at all, a scheme or another spelling of the same host
	// included, which is what the form asks a secret for. Otherwise it goes through the secrets
	// endpoint. A change of host or port, or of a dest leaf, needs every set secret in the same
	// patch; one from the file or the environment cannot be given here at all.
	edited := section != "" && config.DestinationEdited(res, cand, section)
	dest := section != "" && config.DestinationChanged(res, cand, section)
	if len(secrets) > 0 && !edited {
		for _, path := range secrets {
			fields = append(fields, model.SettingsFieldError{Path: path, Message: msgUseSecrets})
		}
		return fieldsReply(fields)
	}
	if dest {
		set := config.SetSecrets(res, section)
		for _, path := range set {
			if src := res.Provenance[path].Source; src == config.SourceEnv || src == config.SourceFile {
				return secretLockedReply(path)
			}
		}
		for _, path := range set {
			if !slices.Contains(secrets, path) {
				fields = append(fields, model.SettingsFieldError{Path: path, Message: msgReenterSecret})
			}
		}
		if len(fields) > 0 {
			return fieldsReply(fields)
		}
	}

	pending, rep := s.settingsIntent(r, actionSettingsSet, settingsTarget(section), paths)
	if rep != nil {
		return rep
	}
	if s.d.Secrets != nil {
		for _, sv := range plain {
			s.d.Secrets.AddString(sv) // scrubbed from logs and answers from now on
		}
	}
	return s.commit(b, pending, paths, sets, p.Deletes, secrets)
}

// commit is the end of every write, under the lock and after its intent was recorded: one Apply
// (scrub lists the paths whose old values leave the backups) and a Reload, the store reverted
// when either fails. The outcome is ok only for a change that is in effect. supplied are the
// paths the request sent, the only ones a reload's problem is reported at.
func (s *Server) commit(b SettingsBackend, pending audit.Pending, supplied []string, sets map[string]any, deletes, scrub []string) *settingsReply {
	prev, err := b.Apply(sets, deletes, scrub)
	if err != nil {
		_ = b.Revert(prev) // a zero prev reverts nothing
		pending.Outcome(errSettingsSave)
		if errors.Is(err, settings.ErrReadOnly) {
			return readonlyReply()
		}
		return internalReply()
	}
	if err := b.Reload(); err != nil {
		return s.revertAfterReload(b, prev, pending, supplied, err)
	}
	pending.Outcome(nil)
	return nil
}

// revertAfterReload undoes a write whose Reload failed (design D15, ruling 2): the value would
// otherwise sit in the store, saved and not applied, and be dropped at the next restart. The
// store gets its previous values back, the reloader reads them again, and the answer is the
// reload's problem at the supplied paths, or invalid_configuration.
func (s *Server) revertAfterReload(b SettingsBackend, prev settings.Snapshot, pending audit.Pending, supplied []string, err error) *settingsReply {
	if rerr := b.Revert(prev); rerr == nil {
		_ = b.Reload() // the previous values were in force; a failure leaves them so
	}
	pending.Outcome(errSettingsReload)
	return s.configReply(err, b, supplied)
}

// settingsTarget names what a settings write changes, for the audit record.
func settingsTarget(section string) audit.Target {
	if id, ok := strings.CutPrefix(section, "plugins."); ok {
		return audit.Target{Kind: "plugin", Name: id}
	}
	return audit.Target{Kind: "settings", Name: "general"}
}

// k8sRefProblem is what is wrong with a k8s: reference given from the UI, "" when nothing: its
// syntax, and its namespace, which must be dash's own or one settings.secretNamespaces lists
// (design D16). References from the file and the environment are the operator's and any
// namespace goes.
func (s *Server) k8sRefProblem(b SettingsBackend, res *config.Resolved, raw string) string {
	ref, err := secret.ParseK8sRef(raw)
	if err != nil {
		return msgBadK8sRef
	}
	if own := b.Namespace(); own != "" && ref.Namespace == own {
		return ""
	}
	if slices.Contains(res.Tree.Settings.SecretNamespaces, ref.Namespace) {
		return ""
	}
	return msgK8sNamespace
}

// deleteSettingsStored answers DELETE /api/v1/settings/plugins/{id}/stored/{field...}: the value
// the store holds for the field goes (scrubbed from the backups for a secret). It is the one
// write allowed on a field the file or the environment sets: it removes a shadowed value.
func (s *Server) deleteSettingsStored(w http.ResponseWriter, r *http.Request) {
	b, id, path, ok := s.settingsField(w, r)
	if !ok || !s.writable(w, r, b) {
		return
	}
	settingsWrite(w, b, func() *settingsReply { return s.removeStored(r, b, id, path) })
}

func (s *Server) removeStored(r *http.Request, b SettingsBackend, id, path string) *settingsReply {
	if rep := checkRevision(r, b, false); rep != nil {
		return rep
	}
	leaf, ok := config.LeafAt(path)
	if !ok {
		return fieldReply(path, msgUnknownField)
	}
	stored := b.Stored()
	if _, held := config.StoredAt(stored, path); !held {
		return nil // nothing stored: nothing to do
	}
	if _, err := b.Candidate(config.UILayer(stored, nil, []string{path})); err != nil {
		return s.configReply(err, b, []string{path})
	}
	pending, rep := s.settingsIntent(r, actionSettingsRemoveStored, settingsTarget("plugins."+id), []string{path})
	if rep != nil {
		return rep
	}
	var scrub []string
	if leaf.Secret {
		scrub = []string{path}
	}
	return s.commit(b, pending, []string{path}, nil, []string{path}, scrub)
}
