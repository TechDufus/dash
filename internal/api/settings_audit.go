package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
)

const (
	auditPageDefault = 100
	auditPageMax     = 500
)

// getSettingsAudit answers GET /api/v1/settings/audit?ring=&limit=&before=: one page of a ring of
// the audit log, newest first. The cursor is opaque and bound to its ring ("main.<cursor>"), so
// one ring's cursor is refused on the other.
func (s *Server) getSettingsAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.settingsBackend(w); !ok {
		return
	}
	q := r.URL.Query()
	ring := audit.RingMain
	if v := q.Get("ring"); v != "" {
		ring = audit.Ring(v)
	}
	if ring != audit.RingMain && ring != audit.RingUnauth {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "ring must be main or unauth")
		return
	}
	limit := auditPageDefault
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "limit must be a whole number from 1 to 500")
			return
		}
		limit = min(n, auditPageMax)
	}
	var before string
	if v := q.Get("before"); v != "" {
		c, ok := strings.CutPrefix(v, string(ring)+".")
		if !ok || c == "" {
			writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "before is not a cursor of this ring")
			return
		}
		before = c
	}
	recs, next, source := s.d.Audit.Recent(limit, before, ring)
	page := model.SettingsAudit{
		Enabled: s.d.Audit.Enabled(), Source: model.AuditSource(source), Ring: model.AuditRing(ring),
		Records: make([]model.AuditEntry, len(recs)),
	}
	if next != "" {
		page.Next = string(ring) + "." + next
	}
	for i, rec := range recs {
		e := model.AuditEntry{
			Time: rec.Time, Kind: model.AuditKind(rec.Kind), ID: rec.ID, Action: rec.Action, Fields: rec.Fields,
			Result: rec.Result, Error: rec.Error, Plugin: rec.Plugin, Reason: rec.Reason, Peer: rec.Peer, RequestID: rec.RequestID,
			Actor: model.AuditActor{Type: rec.Actor.Type, User: rec.Actor.User, Surface: rec.Actor.Surface},
		}
		if rec.Target != (audit.Target{}) {
			e.Target = &model.AuditTarget{Kind: rec.Target.Kind, Namespace: rec.Target.Namespace, Name: rec.Target.Name}
		}
		page.Records[i] = e
	}
	writeJSON(w, http.StatusOK, page)
}
