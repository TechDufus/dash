package api

import (
	"net/http"
	"strings"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

// putSettingsSecret answers PUT /api/v1/settings/plugins/{id}/secrets/{field...}: a value, stored
// encrypted with the old ciphertext scrubbed from the backups, or a k8s: reference. If-Match is
// optional; when sent it must name the current revision.
func (s *Server) putSettingsSecret(w http.ResponseWriter, r *http.Request) {
	b, _, path, ok := s.settingsField(w, r)
	if !ok || !s.writable(w, r, b) {
		return
	}
	var body model.SettingsSecretWrite
	if !s.decodeStrict(w, r, &body) {
		return
	}
	settingsWrite(w, b, func() *settingsReply { return s.replaceSecret(r, b, path, body) })
}

// deleteSettingsSecret answers DELETE /api/v1/settings/plugins/{id}/secrets/{field...}: the
// secret saved from the UI goes, and its ciphertext with it from every backup.
func (s *Server) deleteSettingsSecret(w http.ResponseWriter, r *http.Request) {
	b, _, path, ok := s.settingsField(w, r)
	if !ok || !s.writable(w, r, b) {
		return
	}
	settingsWrite(w, b, func() *settingsReply { return s.clearSecret(r, b, path) })
}

// uiSecret checks that path is a secret the UI may write, and that no file or environment value
// locks it.
func uiSecret(res *config.Resolved, path string) *settingsReply {
	if l, ok := config.LeafAt(path); !ok || !l.Secret || l.Ineligible {
		return fieldReply(path, msgNotUISecret)
	}
	if p := res.Provenance[path]; p.Source == config.SourceEnv || p.Source == config.SourceFile {
		return lockedReply(path, p)
	}
	return nil
}

// replaceSecret writes a secret through the pipeline of every settings write: the intent
// (settings.secret.replace), one Apply that scrubs the old ciphertext from the backups, and a
// Reload, the store reverted and the outcome failed when either does not take.
func (s *Server) replaceSecret(r *http.Request, b SettingsBackend, path string, body model.SettingsSecretWrite) *settingsReply {
	if rep := checkRevision(r, b, false); rep != nil {
		return rep
	}
	res, stored := b.Resolved(), b.Stored()
	if rep := uiSecret(res, path); rep != nil {
		return rep
	}
	ref := strings.TrimSpace(body.Ref)
	switch {
	case (body.Value == "") == (ref == ""):
		return fieldReply(path, msgSecretBody)
	case ref != "" && config.HasReference(ref):
		return fieldReply(path, msgReference)
	case ref != "" && !strings.HasPrefix(ref, "k8s:"):
		return fieldReply(path, msgBadK8sRef)
	case ref != "":
		if rep := operatorRefReply(res, path, ref); rep != nil {
			return rep
		}
		if msg := s.k8sRefProblem(b, res, ref); msg != "" {
			return fieldReply(path, msg)
		}
	case len(body.Value) > maxSecretBytes:
		return fieldReply(path, msgSecretTooLong)
	case res.Keys == nil:
		return noSecretKeyReply()
	}

	// The store holds the sealed value, or the reference.
	next := ref
	if ref == "" {
		sealed, err := res.Keys.Encrypt(path, body.Value)
		if err != nil {
			return noSecretKeyReply()
		}
		next = sealed
	}
	sets := map[string]any{path: next}
	if _, err := b.Candidate(config.UILayer(stored, sets, nil)); err != nil {
		return s.configReply(err, b, []string{path})
	}
	pending, rep := s.settingsIntent(r, actionSecretReplace, settingsTarget(sectionOf(path)), []string{path})
	if rep != nil {
		return rep
	}
	if ref == "" && s.d.Secrets != nil {
		s.d.Secrets.AddString(body.Value) // scrubbed from logs and answers from now on
	}
	return s.commit(b, pending, []string{path}, sets, nil, []string{path})
}

// clearSecret removes a secret saved from the UI through the same pipeline: the intent
// (settings.secret.clear), one Apply that scrubs its ciphertext from the backups, and a Reload.
func (s *Server) clearSecret(r *http.Request, b SettingsBackend, path string) *settingsReply {
	if rep := checkRevision(r, b, false); rep != nil {
		return rep
	}
	res, stored := b.Resolved(), b.Stored()
	if rep := uiSecret(res, path); rep != nil {
		return rep
	}
	if _, held := config.StoredAt(stored, path); !held {
		return nil // nothing saved from the UI: nothing to clear
	}
	if _, err := b.Candidate(config.UILayer(stored, nil, []string{path})); err != nil {
		return s.configReply(err, b, []string{path})
	}
	pending, rep := s.settingsIntent(r, actionSecretClear, settingsTarget(sectionOf(path)), []string{path})
	if rep != nil {
		return rep
	}
	return s.commit(b, pending, []string{path}, nil, []string{path}, []string{path})
}

// sectionOf is plugins.<id> of a path under it.
func sectionOf(path string) string {
	parts := strings.SplitN(path, ".", 3)
	if len(parts) < 2 {
		return path
	}
	return parts[0] + "." + parts[1]
}
