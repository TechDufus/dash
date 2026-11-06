package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/TechDufus/dash/internal/approvals"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	dashmcp "github.com/TechDufus/dash/internal/mcp"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// The action tools' side of the MCP backend (design N.2, O; contract D48–D53).

var _ dashmcp.ActionBackend = mcpBackend{}

// ActionTools lists every action a registered plugin declares, in registry order, as its tool.
func (b mcpBackend) ActionTools() []dashmcp.ActionTool {
	if b.s.d.Plugins == nil {
		return nil
	}
	var out []dashmcp.ActionTool
	for _, m := range b.s.d.Plugins.Manifests() {
		for _, a := range m.Actions {
			out = append(out, dashmcp.ActionTool{
				Name: toolName(m.ID, a.Name), Title: m.Name + ": " + a.Title, Description: actionToolDesc(m, a),
				InputSchema: agentSchema(b.s.inputSchema(a)), Destructive: a.Destructive, Idempotent: a.Idempotent,
			})
		}
	}
	return out
}

// actionToolDesc tells the agent what the tool does and how it is decided.
func actionToolDesc(m plugin.Manifest, a plugin.ActionSpec) string {
	d := a.Title + " (the " + m.Name + " plugin's " + a.Name + " action)."
	if a.Destructive {
		d += " Destructive."
	}
	return d + " Unless the plugin's agent policy is allow, the call does not run the action: it asks the token's " +
		"owner to approve it in dash, and answers how to learn the decision (an approval URL, or an approvalId for " +
		"dash_approval). A target that needs its name typed always waits for the owner's approval."
}

// agentSchema is an action input's schema as an agent sees it: without confirm, which dash fills
// only from a person's decision (contract D49); an object with no properties for an action
// without input.
func agentSchema(schema json.RawMessage) json.RawMessage {
	var m map[string]any
	if len(schema) == 0 || json.Unmarshal(schema, &m) != nil || m == nil {
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for k := range props {
			if strings.EqualFold(k, "confirm") {
				delete(props, k)
			}
		}
	}
	if req, ok := m["required"].([]any); ok {
		m["required"] = slices.DeleteFunc(req, func(v any) bool { s, _ := v.(string); return strings.EqualFold(s, "confirm") })
	}
	out, err := json.Marshal(m)
	if err != nil {
		return schema
	}
	return out
}

// actionOfTool is the mounted action behind tool, if it is mounted now.
func (s *Server) actionOfTool(tool string) (plugin.ActionRef, bool) {
	if s.d.Plugins == nil {
		return plugin.ActionRef{}, false
	}
	for _, m := range s.d.Plugins.Manifests() {
		for _, a := range m.Actions {
			if toolName(m.ID, a.Name) == tool {
				return s.d.Plugins.Action(m.ID, a.Name)
			}
		}
	}
	return plugin.ActionRef{}, false
}

// ActionVisible is the listing rule of contract D48: an administrator's mcp token holding
// actions:<plugin>, the action mounted (actions.enabled, the instance running) and the agent
// policy not deny.
func (b mcpBackend) ActionVisible(p auth.Principal, tool string) bool {
	ref, ok := b.s.actionOfTool(tool)
	return ok && ref.Agents != pluginconf.AgentsDeny && p.Kind == auth.KindToken && p.Audience == model.TokenAudienceMCP &&
		p.Role.AtLeast(auth.RoleAdmin) && authorizeAction(p, ref) == nil
}

// CallAction is an agent's call of an action tool. It is checked first (role, scope, input,
// target), the agent's confirm dropped. Under allow it runs at once with the token's own
// limits, unless the target's rule is typeName (contract D33, D49); otherwise it becomes an
// approval request, or finds the pending one it repeats.
func (b mcpBackend) CallAction(r *http.Request, p auth.Principal, client, tool string, args json.RawMessage) (dashmcp.ActionReply, error) {
	s := b.s
	ref, ok := s.actionOfTool(tool)
	if !ok || ref.Agents == pluginconf.AgentsDeny {
		return dashmcp.ActionReply{}, &dashmcp.Error{Code: model.ErrCodeNotFound, Message: "this action is not offered to agents"}
	}
	client = s.clip(client, maxAuditAction)
	c := agentCall{P: p, ClientName: client, Ref: ref, Args: args, RequestID: requestIDFrom(r.Context()), Peer: peerOf(r)}
	a, d := s.checkAgentAction(r.Context(), c)
	if d != nil {
		return dashmcp.ActionReply{}, s.agentRefusal(r, d)
	}
	if ref.Agents == pluginconf.AgentsAllow && a.Rule != plugin.ConfirmTypeName {
		rec := s.auditRecord(r, ref.Spec.Name, a.Target)
		rec.Plugin, rec.ArgsSHA256, rec.Client = string(ref.Instance), a.SHA256, client
		out, err := s.executeAction(r.Context(), ref, p, a.Input, rec)
		switch {
		case errors.Is(err, errAuditUnavailable):
			return dashmcp.ActionReply{}, &dashmcp.Error{Code: model.ErrCodeAuditUnavailable, Message: "audit log unavailable; the action did not run"}
		case err != nil:
			if ae, ok := errors.AsType[*plugin.ActionError](err); ok {
				return dashmcp.ActionReply{}, &dashmcp.Error{Code: actionErrorCode(ae), Message: ae.Message}
			}
			return dashmcp.ActionReply{}, err
		}
		return dashmcp.ActionReply{Ran: true, Result: out}, nil
	}
	info, _, d := s.requestApproval(c, a)
	if d != nil {
		return dashmcp.ActionReply{}, s.agentRefusal(r, d)
	}
	return dashmcp.ActionReply{Approval: info, ApproveURL: b.ApproveURL(info.ID)}, nil
}

// actionErrorCode is the code of a plugin's refusal: its own when dash knows it, else the one
// its status implies (as writeActionError answers).
func actionErrorCode(ae *plugin.ActionError) string {
	if slices.Contains(model.ErrCodes(), ae.Code) {
		return ae.Code
	}
	return codeForStatus(ae.Status)
}

// agentRefusal records a refusal of an agent's call as the route chain does, and returns it as
// the tool's error.
func (s *Server) agentRefusal(r *http.Request, d *actionDenial) error {
	if d.Reason != (obs.AuthReason{}) {
		s.deny(r, d.Reason)
	}
	return &dashmcp.Error{Code: d.Code, Message: d.Msg}
}

// Approval is dash_approval and the retry of an input-required answer: request id as its owner's
// token reads it. Reading the outcome of a request that ran is recorded (contract D54).
func (b mcpBackend) Approval(r *http.Request, p auth.Principal, id string) (model.ApprovalInfo, error) {
	s := b.s
	if !approvals.WellFormed(id) {
		return model.ApprovalInfo{}, &dashmcp.Error{Code: model.ErrCodeInvalidInput, Message: "id is not an approval id (appr_ and 26 characters)"}
	}
	info, err := s.approvals.Get(id, p.User)
	switch {
	case errors.Is(err, approvals.ErrForbidden):
		return model.ApprovalInfo{}, &dashmcp.Error{Code: model.ErrCodeForbidden, Message: "this approval request is another user's"}
	case err != nil:
		return model.ApprovalInfo{}, &dashmcp.Error{Code: model.ErrCodeApprovalExpired, Message: "this approval request expired or no longer exists"}
	}
	if info.Outcome != nil {
		rec := s.approvalRecord(auditApprovalResult, info, p)
		rec.RequestID, rec.Peer, rec.AuthMode = requestIDFrom(r.Context()), peerOf(r), s.authMode(p)
		rec.Result = audit.ResultOK
		s.d.Audit.Event(rec)
	}
	return info, nil
}

// ApproveURL is the SPA page where request id is decided.
func (b mcpBackend) ApproveURL(id string) string {
	return strings.TrimRight(b.s.d.Cfg.PublicURL, "/") + "/approve/" + id
}

// WaitApproval blocks until request id leaves pending or ctx ends.
func (b mcpBackend) WaitApproval(ctx context.Context, id string) bool {
	return b.s.approvals.Wait(ctx, id)
}
