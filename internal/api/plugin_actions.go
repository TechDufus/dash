package api

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
)

// runAction is POST /api/v1/plugins/{id}/actions/{name}: the request chain every plugin action
// goes through (design A.8). A refusal at any step ends the request before Act runs. The steps
// that do not need the request (the checks, intent, Act, outcome) are in action_exec.go, shared
// with the callers that are not this route.
//
// A token needs the actions:<plugin> scope (403 insufficient_scope), and its actionNamespaces
// restrict the target's namespace beside the instance's DenyNamespaces. CSRF and Origin are
// checked ahead of the mux for every non-GET request that is not a token's.
func (s *Server) runAction(w http.ResponseWriter, r *http.Request) {
	id, name := plugin.ID(r.PathValue("id")), r.PathValue("name")
	var ref plugin.ActionRef
	found := false
	if s.d.Plugins != nil && id.Valid() {
		ref, found = s.d.Plugins.Action(id, name)
	}
	if !found { // unknown instance, not running, actions disabled, not an Actor or undeclared
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such action")
		return
	}
	started, _ := w.(*startedWriter)
	w, done := s.d.Metrics.TrackAction(w, string(id), name)
	defer done()
	// Deferred after done, so it runs first: the 500 is on the tracked writer when done reads
	// the status, and the action counts as an error, not as the 200 of an unwritten response. A
	// panic in Target, Input or Validate happens before the audit intent; the refusal is still
	// recorded.
	defer func() {
		if rec := recover(); rec != nil {
			s.refuse(r, "internal")
			s.answerPanic(w, started, r, rec)
		}
	}()
	p := principalFrom(r.Context())

	if d := authorizeAction(p, ref); d != nil {
		s.denyAction(w, r, d)
		return
	}

	if ok, retry := s.actionLimit.allow(p.User, s.now()); !ok {
		s.deny(r, obs.AuthRateLimit)
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
		writeErr(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "too many actions; try again later")
		return
	}

	input, ok := s.actionInput(w, r, ref.Spec)
	if !ok {
		return
	}

	target := actionTarget(ref, input)
	if d := authorizeTarget(p, ref, target); d != nil {
		s.denyAction(w, r, d)
		return
	}

	rec := s.auditRecord(r, name, target)
	rec.Plugin = string(id)
	out, err := s.executeAction(r.Context(), ref, p, input, rec)
	switch {
	case errors.Is(err, errAuditUnavailable):
		writeErr(w, http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable, "audit log unavailable")
	case err == nil && out == nil:
		accepted(w)
	case err == nil:
		writeJSON(w, http.StatusOK, out)
	default:
		var ae *plugin.ActionError
		if errors.As(err, &ae) {
			s.writeActionError(w, ae)
			return
		}
		s.badGateway(w, string(id), err)
	}
}

// denyAction records and answers a refusal of the action chain.
func (s *Server) denyAction(w http.ResponseWriter, r *http.Request, d *actionDenial) {
	s.deny(r, d.Reason)
	writeErr(w, d.Status, d.Code, d.Msg)
}

// accepted answers an action that returns nothing: 202 {"status":"accepted"}.
func accepted(w http.ResponseWriter) {
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}

// unwrapOutcome splits a plugin.Outcome (or a pointer to one) into its body and its audit target;
// any other result is its own body with no target.
func unwrapOutcome(out any) (any, *plugin.ActionTarget) {
	switch o := out.(type) {
	case plugin.Outcome:
		return o.Body, o.Target
	case *plugin.Outcome:
		if o != nil {
			return o.Body, o.Target
		}
		return nil, nil
	}
	return out, nil
}

// actionInput reads and checks the request body of an action into a pointer to a fresh copy of
// the struct the spec's Input returns. It answers 400 itself and reports false on a body that
// is not JSON, names a field the struct does not have, breaks a dash tag (required, enum) or
// fails the input's own Validate. An action without an Input takes no body.
func (s *Server) actionInput(w http.ResponseWriter, r *http.Request, spec plugin.ActionSpec) (any, bool) {
	ptr, ok := newActionInput(spec)
	if !ok {
		return nil, true
	}
	if !s.decodeStrict(w, r, ptr.Interface()) {
		return nil, false
	}
	if msg := checkActionInput(ptr); msg != "" {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, msg)
		return nil, false
	}
	return ptr.Interface(), true
}

// writeActionError answers a deliberate refusal from Act with the status and code the plugin
// chose. A status that is not an error status is the plugin's mistake: the status becomes 500
// and the code internal. A missing code, or one outside model.ErrCodes (the set the OpenAPI
// document declares), becomes the code the status implies.
func (s *Server) writeActionError(w http.ResponseWriter, ae *plugin.ActionError) {
	status, code := ae.Status, ae.Code
	if status < 400 || status > 599 {
		status, code = http.StatusInternalServerError, model.ErrCodeInternal
	}
	if !slices.Contains(model.ErrCodes(), code) {
		code = codeForStatus(status)
	}
	writeJSON(w, status, model.APIError{
		Error: s.d.Secrets.Scrub(ae.Message), Code: code, Details: ae.Details, RequestID: w.Header().Get(requestIDHeader),
	})
}

// codeForStatus is the default code of a status, for an answer whose author named none.
func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return model.ErrCodeInvalidInput
	case http.StatusUnauthorized:
		return model.ErrCodeUnauthenticated
	case http.StatusForbidden:
		return model.ErrCodeForbidden
	case http.StatusNotFound:
		return model.ErrCodeNotFound
	case http.StatusConflict:
		return model.ErrCodeConflict
	case http.StatusTooManyRequests:
		return model.ErrCodeRateLimited
	case http.StatusBadGateway, http.StatusGatewayTimeout:
		return model.ErrCodeUpstreamUnavailable
	}
	return model.ErrCodeInternal
}

// validateInput checks a decoded action input against its dash tags: a `required` field must
// not be the zero value and an `enum=a|b` field must be one of the values (the rules the
// reflected input schema states). It returns the first problem as text, or "".
func validateInput(v reflect.Value) string {
	for v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if msg := validateInput(v.Index(i)); msg != "" {
				return msg
			}
		}
	case reflect.Struct:
		t := v.Type()
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fv := v.Field(i)
			for _, opt := range strings.Split(f.Tag.Get("dash"), ",") {
				switch key, val, _ := strings.Cut(opt, "="); key {
				case "required":
					if fv.IsZero() {
						return name + " is required"
					}
				case "enum":
					if fv.IsZero() {
						continue
					}
					// A pointer field is compared by the value it points to, as its schema says.
					ev := fv
					for ev.Kind() == reflect.Pointer && !ev.IsNil() {
						ev = ev.Elem()
					}
					if got := fmt.Sprint(ev.Interface()); !slices.Contains(strings.Split(val, "|"), got) {
						return fmt.Sprintf("%s must be one of %s", name, strings.ReplaceAll(val, "|", ", "))
					}
				}
			}
			if msg := validateInput(fv); msg != "" {
				return msg
			}
		}
	}
	return ""
}
