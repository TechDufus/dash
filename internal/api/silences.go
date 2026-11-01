package api

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/upstream"
)

const (
	maxMatchers = 20
	maxComment  = 500
)

var silenceDurations = map[string]time.Duration{
	"1h":  time.Hour,
	"4h":  4 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// validateSilence checks a request and returns its duration and trimmed comment.
func validateSilence(req model.SilenceRequest) (time.Duration, string, string) {
	dur, ok := silenceDurations[req.Duration]
	if !ok {
		return 0, "", "duration must be one of 1h, 4h, 24h, 7d"
	}
	if len(req.Matchers) == 0 || len(req.Matchers) > maxMatchers {
		return 0, "", "need between 1 and 20 matchers"
	}
	hasAlertname := false
	for _, m := range req.Matchers {
		if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Value) == "" {
			return 0, "", "matcher name and value must not be empty"
		}
		hasAlertname = hasAlertname || m.Name == "alertname"
	}
	if !hasAlertname {
		return 0, "", "an alertname matcher is required"
	}
	comment := strings.TrimSpace(req.Comment)
	if n := utf8.RuneCountInString(comment); n < 1 || n > maxComment {
		return 0, "", "comment must be 1-500 characters"
	}
	return dur, comment, ""
}

func (s *Server) createSilence(w http.ResponseWriter, r *http.Request) {
	var req model.SilenceRequest
	if !s.decodeBody(w, r, &req) {
		return
	}
	dur, comment, msg := validateSilence(req)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	alertname := ""
	for _, m := range req.Matchers {
		if m.Name == "alertname" {
			alertname = m.Value
			break
		}
	}
	now := s.now()
	id, err := s.d.AM.CreateSilence(r.Context(), upstream.SilenceSpec{
		Matchers:  req.Matchers,
		StartsAt:  now,
		EndsAt:    now.Add(dur),
		CreatedBy: "dash:" + userFrom(r.Context()),
		Comment:   comment,
	})
	name := id
	if err != nil {
		name = alertname
	}
	audit(r, "silence.create", "Silence", "", name, err)
	switch {
	case errInvalid(err):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		s.badGateway(w, "alertmanager", err)
	default:
		writeJSON(w, http.StatusOK, model.SilenceResponse{SilenceID: id})
	}
}

func (s *Server) expireSilence(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := s.d.AM.ExpireSilence(r.Context(), id)
	audit(r, "silence.expire", "Silence", "", id, err)
	switch {
	case errInvalid(err):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		s.badGateway(w, "alertmanager", err)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "expired"})
	}
}
