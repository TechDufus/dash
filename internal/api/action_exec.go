package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
)

// The action chain without its transport (design A.8): the checks of the principal, of the
// input and of the target, then intent, Act and outcome. The HTTP route (runAction), an agent's
// call over MCP and the run of an approved request share these steps; each transport turns a
// refusal into its own answer.

// actionDenial is a refusal of the chain before Act: what the HTTP route answers, and the reason
// the refusal is recorded and counted with.
type actionDenial struct {
	Reason obs.AuthReason
	Status int
	Code   string
	Msg    string
}

// authorizeAction checks the principal against the action: its minimum role, then the
// actions:<plugin> scope. nil lets the call go on.
func authorizeAction(p auth.Principal, ref plugin.ActionRef) *actionDenial {
	if !p.Role.AtLeast(ref.Spec.MinRole) {
		return &actionDenial{Reason: obs.AuthForbidden, Status: http.StatusForbidden, Code: model.ErrCodeForbidden, Msg: "forbidden"}
	}
	if scope := model.TokenScopeActionsPrefix + string(ref.Instance); !p.HasScope(scope) {
		return &actionDenial{
			Reason: obs.AuthInsufficientScope, Status: http.StatusForbidden, Code: model.ErrCodeInsufficientScope,
			Msg: "this token lacks the " + scope + " scope",
		}
	}
	return nil
}

// actionTarget is the audit target of an input, as the action's spec names it.
func actionTarget(ref plugin.ActionRef, input any) audit.Target {
	if ref.Spec.Target == nil {
		return audit.Target{}
	}
	kind, ns, name := ref.Spec.Target(input)
	return audit.Target{Kind: kind, Namespace: ns, Name: name}
}

// authorizeTarget checks the target's namespace against the instance's DenyNamespaces and the
// token's actionNamespaces. nil lets the call go on.
func authorizeTarget(p auth.Principal, ref plugin.ActionRef, t audit.Target) *actionDenial {
	if t.Namespace != "" && slices.Contains(ref.DenyNamespaces, t.Namespace) {
		return &actionDenial{
			Reason: obs.AuthNamespaceDenied, Status: http.StatusForbidden, Code: model.ErrCodeForbidden,
			Msg: "actions are not allowed in this namespace",
		}
	}
	if !p.ActionNamespaceAllowed(t.Namespace) {
		return &actionDenial{
			Reason: obs.AuthNamespaceDenied, Status: http.StatusForbidden, Code: model.ErrCodeForbidden,
			Msg: "this token may not act in this namespace",
		}
	}
	return nil
}

// newActionInput is a pointer to a fresh zero copy of the struct the spec's Input returns; false
// for an action that takes no input.
func newActionInput(spec plugin.ActionSpec) (reflect.Value, bool) {
	if spec.Input == nil {
		return reflect.Value{}, false
	}
	t := reflect.TypeOf(spec.Input())
	if t == nil {
		return reflect.Value{}, false
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return reflect.New(t), true
}

// checkActionInput checks a decoded input against its dash tags (required, enum), then its own
// Validate. It returns the first problem as text for a 400 invalid_input, or "".
func checkActionInput(ptr reflect.Value) string {
	if msg := validateInput(ptr); msg != "" {
		return msg
	}
	if c, ok := ptr.Interface().(interface{ Validate() error }); ok {
		if err := c.Validate(); err != nil {
			return err.Error()
		}
	}
	return ""
}

// decodeActionInput is the route's body decode for a caller that already holds the bytes (an MCP
// tool call, an approval request): the first JSON value of data, decoded strictly (a field the
// struct does not have is refused), then checked. It returns the input, or the problem as text
// for a 400 invalid_input. An action without an Input takes none and returns nil.
func decodeActionInput(spec plugin.ActionSpec, data []byte) (any, string) {
	ptr, ok := newActionInput(spec)
	if !ok {
		return nil, ""
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(ptr.Interface()); err != nil {
		return nil, "invalid request body"
	}
	if msg := checkActionInput(ptr); msg != "" {
		return nil, msg
	}
	return ptr.Interface(), ""
}

// errAuditUnavailable is executeAction's answer when the intent could not be written: Act did
// not run. The HTTP route answers it 503 audit_unavailable.
var errAuditUnavailable = errors.New("audit log unavailable")

// executeAction runs a checked call: the audit intent rec (fail-closed: errAuditUnavailable and
// no run), then Act as p under ctx, then the outcome. An action whose audit target exists only
// after it ran (plugin.Outcome) re-targets the record before its outcome is written, on success
// only. It returns Act's body, unwrapped, and Act's error.
func (s *Server) executeAction(ctx context.Context, ref plugin.ActionRef, p auth.Principal, input any, rec audit.Record) (any, error) {
	pending, err := s.d.Audit.Intent(rec)
	if err != nil {
		return nil, errAuditUnavailable
	}
	out, err := ref.Run(ctx, p, input)
	if err == nil {
		var t *plugin.ActionTarget
		if out, t = unwrapOutcome(out); t != nil {
			pending = pending.WithTarget(audit.Target{Kind: t.Kind, Namespace: t.Namespace, Name: t.Name})
		}
	}
	pending.Outcome(err)
	return out, err
}
