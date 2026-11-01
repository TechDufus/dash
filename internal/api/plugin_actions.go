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

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// runAction is POST /api/v1/plugins/{id}/actions/{name}: the request chain every plugin action
// goes through (design A.8). A refusal at any step ends the request before Act runs.
//
// The steps of the design that need a bearer token or an agent (the actions:<plugin> scope, a
// token's actionNamespaces, the agent policy) have no principal to apply to yet; phase 7 adds
// them here. CSRF and Origin are checked ahead of the mux for every non-GET request.
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
	w, done := s.d.Metrics.TrackAction(w, string(id), name)
	defer done()
	p := principalFrom(r.Context())

	if !p.Role.AtLeast(ref.Spec.MinRole) {
		s.deny(r, "forbidden")
		writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "forbidden")
		return
	}

	if ok, retry := s.actionLimit.allow(p.User, s.now()); !ok {
		s.deny(r, "rate_limit")
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(retry.Seconds()))))
		writeErr(w, http.StatusTooManyRequests, model.ErrCodeRateLimited, "too many actions; try again later")
		return
	}

	input, ok := s.actionInput(w, r, ref.Spec)
	if !ok {
		return
	}

	var target audit.Target
	if ref.Spec.Target != nil {
		kind, ns, tname := ref.Spec.Target(input)
		target = audit.Target{Kind: kind, Namespace: ns, Name: tname}
	}
	if target.Namespace != "" && slices.Contains(ref.DenyNamespaces, target.Namespace) {
		s.deny(r, "namespace_denied")
		writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "actions are not allowed in this namespace")
		return
	}

	rec := s.auditRecord(r, name, target)
	rec.Plugin = string(id)
	pending, ok := s.beginRecord(w, rec)
	if !ok {
		return
	}
	out, err := ref.Run(r.Context(), p, input)
	pending.Outcome(err)
	switch {
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

// actionInput reads and checks the request body of an action into a pointer to a fresh copy of
// the struct the spec's Input returns. It answers 400 itself and reports false on a body that
// is not JSON, names a field the struct does not have, breaks a dash tag (required, enum) or
// fails the input's own Validate. An action without an Input takes no body.
func (s *Server) actionInput(w http.ResponseWriter, r *http.Request, spec plugin.ActionSpec) (any, bool) {
	if spec.Input == nil {
		return nil, true
	}
	t := reflect.TypeOf(spec.Input())
	if t == nil {
		return nil, true
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	ptr := reflect.New(t)
	if !s.decodeStrict(w, r, ptr.Interface()) {
		return nil, false
	}
	if msg := validateInput(ptr); msg != "" {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, msg)
		return nil, false
	}
	if c, ok := ptr.Interface().(interface{ Validate() error }); ok {
		if err := c.Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, err.Error())
			return nil, false
		}
	}
	return ptr.Interface(), true
}

// writeActionError answers a deliberate refusal from Act with the status and code the plugin
// chose. A status that is not an error status, or a missing code, is the plugin's mistake:
// the status becomes 500 and the code the one the status implies.
func (s *Server) writeActionError(w http.ResponseWriter, ae *plugin.ActionError) {
	status, code := ae.Status, ae.Code
	if status < 400 || status > 599 {
		status, code = http.StatusInternalServerError, model.ErrCodeInternal
	}
	if code == "" {
		code = codeForStatus(status)
	}
	writeJSON(w, status, model.APIError{Error: s.d.Secrets.Scrub(ae.Message), Code: code, Details: ae.Details})
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
					if got := fmt.Sprint(fv.Interface()); !slices.Contains(strings.Split(val, "|"), got) {
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
