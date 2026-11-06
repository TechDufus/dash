package api

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/TechDufus/dash/internal/approvals"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// The audit actions of the approvals (contract D54). A request's creation, expiry and the fetch
// of its result are events; a decision is an audited write (intent, then outcome); the run of
// an approved action is recorded like any action (its name, intent and outcome) with the
// approval's id and approver.
const (
	auditApprovalCreate  = "approval.create"
	auditApprovalApprove = "approval.approve"
	auditApprovalDeny    = "approval.deny"
	auditApprovalExpire  = "approval.expire"
	auditApprovalResult  = "approval.result"
)

// approvalRunTimeout bounds the run of an approved action. It does not end with the deciding
// request: a phone that loses its connection does not stop a half-done action (contract D52). A
// variable for the tests.
var approvalRunTimeout = 60 * time.Second

// approvalSweep is how often the store expires requests nobody touches, so the expiry reaches
// the owner's core.approvals and the audit log in time.
const approvalSweep = 15 * time.Second

// TokenReviver re-reads a token principal's credential (internal/tokens.Resolver): an approved
// action runs as the requester as they stand when it is approved (contract D52), and the
// approval page names the credential.
type TokenReviver interface {
	Revive(p auth.Principal) (auth.Principal, error)
	CredentialName(p auth.Principal) string
}

// newApprovals is the server's request store: its transitions publish the owner's
// core.approvals and record expiries.
func (s *Server) newApprovals() *approvals.Store {
	return approvals.New(approvals.Options{
		Now:     func() time.Time { return s.now() },
		Changed: s.publishApprovals,
		Expired: s.auditApprovalExpired,
	})
}

// RunApprovals expires the requests nobody touches until ctx ends.
func (s *Server) RunApprovals(ctx context.Context) { s.approvals.Run(ctx, approvalSweep) }

// publishApprovals publishes user's pending requests on core.approvals, for that user only. The
// entries leave out the input: the topic is a list, its size bounded by the 20 requests and their
// clipped texts, and the approval page reads the input through GET /api/v1/approvals/{id}. One
// user's publishes are serialised, read and publish together, so two transitions racing cannot
// leave the older list as the last one published.
func (s *Server) publishApprovals(user string) {
	if s.d.Hub == nil {
		return
	}
	mu := s.approvalsPublishLock(user)
	mu.Lock()
	defer mu.Unlock()
	pending := s.approvals.Pending(user)
	for i := range pending {
		pending[i].Input = nil
	}
	if s.approvalsPublishGap != nil {
		s.approvalsPublishGap(user)
	}
	if _, err := s.d.Hub.PublishFor(user, model.TopicApprovals, model.ApprovalsData{Pending: pending}, nil); err != nil {
		slog.Error("core.approvals publish failed", "error", err)
	}
}

// approvalsPublishLock is user's publish lock. The table holds one entry per user who ever had
// a request, which the token and grant owners bound.
func (s *Server) approvalsPublishLock(user string) *sync.Mutex {
	mu, _ := s.approvalsPub.LoadOrStore(user, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// maxApprovalTarget caps each text of an agent action's target and maxApprovalSummary its summary
// (they come from the agent's input). The target's cap is above the 253 bytes of a DNS name, so a
// name a person must type is never cut.
const (
	maxApprovalTarget  = 256
	maxApprovalSummary = 512
)

// clipTarget is t with each text cut to maxApprovalTarget.
func (s *Server) clipTarget(t audit.Target) audit.Target {
	return audit.Target{Kind: s.clip(t.Kind, maxApprovalTarget), Namespace: s.clip(t.Namespace, maxApprovalTarget), Name: s.clip(t.Name, maxApprovalTarget)}
}

// approvalRecord is the audit record of a request's transition: the requester as actor on its
// surface, the target, the plugin, the request's id and its input's hash, never the input.
func (s *Server) approvalRecord(action string, info model.ApprovalInfo, requester auth.Principal) audit.Record {
	return audit.Record{
		Action: action, Target: audit.Target{Kind: info.Target.Kind, Namespace: info.Target.Namespace, Name: info.Target.Name},
		Actor: actorOf(requester, s.authMode(requester)), AuthMode: s.authMode(requester), Plugin: info.Plugin,
		Approval: info.ID, ArgsSHA256: info.InputSHA256, Client: info.ClientName,
	}
}

func (s *Server) auditApprovalExpired(info model.ApprovalInfo) {
	s.d.Audit.Event(s.approvalRecord(auditApprovalExpire, info, approvalRequester(info)))
}

// approvalRequester is the principal an approval record names when the store gives only the
// request: the requester's token or grant on the mcp surface.
func approvalRequester(info model.ApprovalInfo) auth.Principal {
	return auth.Principal{Kind: auth.KindToken, User: info.User, TokenID: info.TokenID, GrantID: info.GrantID, Audience: model.TokenAudienceMCP}
}

// agentCall is an agent's call of an action tool (MCP): who called, through which client, the
// action, and its arguments as sent.
type agentCall struct {
	P          auth.Principal
	ClientName string
	Ref        plugin.ActionRef
	Args       json.RawMessage
	RequestID  string
	Peer       string
}

// agentAction is an agent's call checked and made canonical: the input it runs with (the agent's
// confirm cleared), the input's canonical JSON without confirm and its hash, the audit target,
// and the target's confirmation rule.
type agentAction struct {
	Input  any
	JSON   json.RawMessage
	SHA256 string
	Target audit.Target
	Rule   plugin.Confirm
}

// checkAgentAction runs the chain's checks for an agent's call (contract D50: validate first):
// an administrator, the action's role and the actions:<plugin> scope, arguments no larger than a
// REST body (64 KiB), the input (strictly decoded, its tags and Validate), the target's namespace
// against the instance's and the token's lists. The agent's own confirm is cleared: dash fills it
// only from a person's decision (D33). The returned target is clipped (maxApprovalTarget).
func (s *Server) checkAgentAction(ctx context.Context, c agentCall) (agentAction, *actionDenial) {
	if !c.P.Role.AtLeast(auth.RoleAdmin) {
		return agentAction{}, &actionDenial{Reason: obs.AuthForbidden, Status: http.StatusForbidden, Code: model.ErrCodeForbidden, Msg: "agent actions need an administrator's token"}
	}
	if d := authorizeAction(c.P, c.Ref); d != nil {
		return agentAction{}, d
	}
	if len(c.Args) > maxBody {
		return agentAction{}, &actionDenial{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Msg: "the arguments are larger than 64 KiB"}
	}
	input, msg := decodeActionInput(c.Ref.Spec, c.Args)
	if msg != "" {
		return agentAction{}, &actionDenial{Status: http.StatusBadRequest, Code: model.ErrCodeInvalidInput, Msg: msg}
	}
	setConfirm(input, "")
	target := actionTarget(c.Ref, input)
	if d := authorizeTarget(c.P, c.Ref, target); d != nil {
		return agentAction{}, d
	}
	canon, sum, err := canonicalInput(input)
	if err != nil {
		return agentAction{}, &actionDenial{Status: http.StatusInternalServerError, Code: model.ErrCodeInternal, Msg: "internal error"}
	}
	return agentAction{Input: input, JSON: canon, SHA256: sum, Target: s.clipTarget(target), Rule: c.Ref.ConfirmFor(ctx, input)}, nil
}

// requestApproval creates the approval request of a checked agent call, or finds the pending one
// it duplicates (created false). A new request is recorded, published to its owner and pushed to
// the owner's devices that take approvals (contract D55). Past 20 pending requests the answer is
// 429 rate_limited.
func (s *Server) requestApproval(c agentCall, a agentAction) (model.ApprovalInfo, bool, *actionDenial) {
	spec := c.Ref.Spec
	summary := ""
	if spec.Summary != nil {
		summary = s.clip(spec.Summary(a.Input), maxApprovalSummary)
	}
	credName := ""
	if rv, ok := s.d.Tokens.(TokenReviver); ok {
		credName = rv.CredentialName(c.P)
	}
	info, created, err := s.approvals.Create(approvals.Request{
		User: c.P.User, TokenID: c.P.TokenID, GrantID: c.P.GrantID, CredentialName: credName, ClientName: c.ClientName,
		Plugin: string(c.Ref.Instance), Action: spec.Name, Title: spec.Title, Tool: toolName(c.Ref.Instance, spec.Name),
		Summary: summary, Target: model.ApprovalTarget{Kind: a.Target.Kind, Namespace: a.Target.Namespace, Name: a.Target.Name},
		Input: a.JSON, InputSHA256: a.SHA256, Destructive: spec.Destructive, Confirm: a.Rule, Requester: c.P,
	})
	if errors.Is(err, approvals.ErrTooMany) {
		return model.ApprovalInfo{}, false, &actionDenial{
			Reason: obs.AuthRateLimit, Status: http.StatusTooManyRequests, Code: model.ErrCodeRateLimited,
			Msg: "too many approval requests wait for a decision (20); decide some first",
		}
	}
	if err != nil {
		return model.ApprovalInfo{}, false, &actionDenial{Status: http.StatusInternalServerError, Code: model.ErrCodeInternal, Msg: "internal error"}
	}
	if !created {
		return info, false, nil
	}
	rec := s.approvalRecord(auditApprovalCreate, info, c.P)
	rec.RequestID, rec.Peer = c.RequestID, c.Peer
	s.d.Audit.Event(rec)
	if s.d.Push != nil {
		s.d.Push.Notify(info.User, model.PushMessage{
			Type: model.PushTypeApproval, ID: info.ID, Title: "dash: approve " + info.Title + "?",
			Body: approvalPushBody(info), URL: s.d.Push.URL("/approve/" + info.ID), TS: s.now().UTC(),
		})
	}
	return info, true, nil
}

// approvalPushBody is the text of an approval push: the action and its target, which are lab
// names (contract D55), and who asks.
func approvalPushBody(info model.ApprovalInfo) string {
	who := info.CredentialName
	if info.ClientName != "" {
		who = info.CredentialName + ", with " + info.ClientName + " (as it names itself)"
	}
	return strings.TrimSpace(info.Summary + " - asked by " + who + ". Open dash to approve or deny.")
}

// toolName is the MCP tool of an action: <plugin>_<action with . replaced by _>.
func toolName(instance plugin.ID, action string) string {
	return string(instance) + "_" + strings.ReplaceAll(action, ".", "_")
}

// approvalRoutes are the approval routes (design O.3, contract D51): the owner reads and decides
// a request, from a browser (cookie, CSRF-checked) or with a token holding the approvals scope
// (the dash-android grant). Any other user gets 403; an unknown or expired request 410.
func (s *Server) approvalRoutes() []Route {
	row := func(r Route) Route {
		r.Role, r.Scope, r.Tag = auth.RoleViewer, model.OAuthScopeApprovals, "Approvals"
		return r
	}
	id := []plugin.Param{{Name: "id", Doc: "The request's ID, appr_<26 base32>.", Required: true}}
	return []Route{
		row(Route{
			ID: "getApproval", Method: "GET", Path: "/api/v1/approvals/{id}",
			Summary: "Read an approval request",
			Description: "One of the caller's approval requests, with the action's outcome once approved. A request that was decided or expired stays readable (state approved, denied or expired) for 10 minutes after its expiry; " +
				"403 forbidden for another user's; 404 for an id that is not one; 410 approval_expired after that, or for an id dash does not know (requests do not survive a restart).",
			PathParams: id, Response: model.ApprovalInfo{}, Handler: s.getApproval,
		}),
		row(Route{
			ID: "decideApproval", Method: "POST", Path: "/api/v1/approvals/{id}/decision",
			Summary: "Approve or deny an approval request",
			Description: "Approving runs the action at once, as the agent's credential stands now, and answers the request with its outcome; " +
				"when that credential lost what the action needs, the request is denied with the reason. " +
				"400 invalid_input when the request needs the target's name typed and confirm is not it; 403 forbidden for another user's; " +
				"404 for an id that is not one; 409 conflict when it is no longer pending; 410 approval_expired; 503 audit_unavailable (nothing ran).",
			PathParams: id, Request: model.ApprovalDecision{}, Response: model.ApprovalInfo{}, Handler: s.decideApproval,
		}),
	}
}

// writeApprovalErr answers a store error.
func (s *Server) writeApprovalErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, approvals.ErrForbidden):
		s.deny(r, obs.AuthForbidden)
		writeErr(w, http.StatusForbidden, model.ErrCodeForbidden, "this approval request is another user's")
	case errors.Is(err, approvals.ErrNotPending):
		writeErr(w, http.StatusConflict, model.ErrCodeConflict, "this approval request was already decided")
	default:
		writeErr(w, http.StatusGone, model.ErrCodeApprovalExpired, "this approval request expired or no longer exists")
	}
}

// approvalID is the request id of the path; a malformed one is 404.
func approvalID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !approvals.WellFormed(id) {
		writeErr(w, http.StatusNotFound, model.ErrCodeNotFound, "no such approval request")
		return "", false
	}
	return id, true
}

func (s *Server) getApproval(w http.ResponseWriter, r *http.Request) {
	id, ok := approvalID(w, r)
	if !ok {
		return
	}
	info, err := s.approvals.Get(id, principalFrom(r.Context()).User)
	if err != nil {
		s.writeApprovalErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// decideApproval is POST /api/v1/approvals/{id}/decision. The claim makes the decision once: a
// second one, concurrent or later, is 409. The decision is recorded before anything happens
// (fail-closed 503, and the request stays pending); an approval then runs the action.
func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	id, ok := approvalID(w, r)
	if !ok {
		return
	}
	var body model.ApprovalDecision
	if !s.decodeStrict(w, r, &body) {
		return
	}
	p := principalFrom(r.Context())
	req, err := s.approvals.Claim(id, p.User)
	if err != nil {
		s.writeApprovalErr(w, r, err)
		return
	}
	// Whatever ends this handler without a decision (a refusal, an audit failure, a panic) gives
	// the request back to pending, so it can still be decided or expire.
	finished := false
	defer func() {
		if !finished {
			s.approvals.Release(id)
		}
	}()
	if body.Approve && req.Confirm == plugin.ConfirmTypeName && body.Confirm != req.Target.Name {
		writeErr(w, http.StatusBadRequest, model.ErrCodeInvalidInput, "type the "+req.Target.Kind+"'s name ("+req.Target.Name+") to approve")
		return
	}
	action := auditApprovalDeny
	if body.Approve {
		action = auditApprovalApprove
	}
	rec := s.auditRecord(r, action, audit.Target{Kind: req.Target.Kind, Namespace: req.Target.Namespace, Name: req.Target.Name})
	rec.Plugin, rec.Approval, rec.ArgsSHA256, rec.Client = req.Plugin, req.ID, req.InputSHA256, req.ClientName
	pending, ok := s.beginRecord(w, rec)
	if !ok {
		return
	}
	if !body.Approve {
		info, err := s.approvals.Deny(id, p.User, "")
		finished = err == nil
		pending.Outcome(err)
		if err != nil {
			s.writeApprovalErr(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, info)
		return
	}
	info, err := s.runApproved(r, req, p.User, body.Confirm)
	// A request whose action ran is never given back, even when dash failed to record how.
	finished = info.ID != "" || errors.Is(err, errAfterRun)
	pending.Outcome(err)
	switch {
	case errors.Is(err, errAuditUnavailable):
		writeErr(w, http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable, "audit log unavailable")
	case errors.Is(err, errApprovalPanic), errors.Is(err, errAfterRun) && info.ID == "":
		writeErr(w, http.StatusInternalServerError, model.ErrCodeInternal, "internal error")
	case info.ID == "": // the store refused the end of a claim it gave: nothing to show
		s.writeApprovalErr(w, r, err)
	default:
		writeJSON(w, http.StatusOK, info)
	}
}

// errApprovalPanic is runApproved's answer when a plugin's code panicked before the action ran
// (its Validate, Target or ConfirmFor): nothing ran and the request goes back to pending.
var errApprovalPanic = errors.New("internal error before the action ran")

// errAfterRun is runApproved's answer when dash failed after the action ran: the request is
// finished as approved with a failed outcome, and never runs again.
var errAfterRun = errors.New("dash failed after the action ran")

// runApproved runs a claimed request its owner approved (contract D52). The requester is
// resolved again, and the request is denied with the reason when the credential is gone, its
// owner is no longer an administrator or lost the scope, the action is no longer offered or its
// agent policy is deny, the input is no longer valid, or the target is refused. confirm is the
// name the owner typed, the only confirm the action ever gets. The action runs under its own
// timeout, not the deciding request's, through the shared executor (intent, Act, outcome) with
// the approval's id and approver; errAuditUnavailable means it did not run and the request is
// still claimed. The returned error is what the decision record's outcome notes.
//
// A panic before the action ran gives errApprovalPanic (the caller releases the request); a panic
// after it ran finishes the request as approved with a failed outcome, so it never runs again.
func (s *Server) runApproved(r *http.Request, req approvals.Request, by, confirm string) (info model.ApprovalInfo, err error) {
	ran := false
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		msg := s.d.Secrets.Scrub(fmt.Sprint(rec))
		slog.Error("panic while running an approval", "approval", req.ID, "plugin", req.Plugin, "action", req.Action,
			"ran", ran, "panic", msg, "stack", string(debug.Stack()))
		info, err = model.ApprovalInfo{}, errApprovalPanic
		if ran {
			info, err = s.finishAfterPanic(req, by)
		}
	}()
	deny := func(reason string) (model.ApprovalInfo, error) {
		info, err := s.approvals.Deny(req.ID, "", reason)
		if err != nil {
			return info, err
		}
		return info, errors.New("not run: " + reason)
	}
	if !s.mcpEnabled() {
		return deny("MCP is off (mcp.enabled), so agents' actions do not run")
	}
	rv, ok := s.d.Tokens.(TokenReviver)
	if !ok {
		return deny("the agent's credential can no longer be checked")
	}
	rp, err := rv.Revive(req.Requester)
	if err != nil {
		return deny("the agent's token or grant was revoked or expired, or its owner no longer has a role")
	}
	var ref plugin.ActionRef
	found := false
	if s.d.Plugins != nil {
		ref, found = s.d.Plugins.Action(plugin.ID(req.Plugin), req.Action)
	}
	switch {
	case !found:
		return deny("the action is no longer offered")
	case !s.pluginServing(ref.Instance):
		return deny("the " + req.Plugin + " plugin is not running")
	case ref.Agents == pluginconf.AgentsDeny:
		return deny("the plugin's agent policy is now deny")
	case !rp.Role.AtLeast(auth.RoleAdmin):
		return deny("the agent's owner is no longer an administrator")
	}
	if d := authorizeAction(rp, ref); d != nil {
		return deny("the agent's credential may no longer run this action: " + d.Msg)
	}
	input, msg := decodeActionInput(ref.Spec, req.Input)
	if msg != "" {
		return deny("the input is no longer valid: " + msg)
	}
	target := actionTarget(ref, input)
	if d := authorizeTarget(rp, ref, target); d != nil {
		return deny(d.Msg)
	}
	// The rule may have tightened since the request (a namespace newly protected): the typed name
	// is then required now too, and only the owner's goes into the input.
	if (req.Confirm == plugin.ConfirmTypeName || ref.ConfirmFor(r.Context(), input) == plugin.ConfirmTypeName) && confirm != target.Name {
		return deny("the target now needs its name typed; ask the agent to try again")
	}
	setConfirm(input, confirm)

	mode := s.authMode(rp)
	rec := audit.Record{
		Action: ref.Spec.Name, Target: target, Actor: actorOf(rp, mode), AuthMode: mode, Peer: peerOf(r),
		RequestID: requestIDFrom(r.Context()), Plugin: req.Plugin, Approval: req.ID, ApprovedBy: by,
		ArgsSHA256: req.InputSHA256, Client: req.ClientName,
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), approvalRunTimeout)
	defer cancel()
	out, runErr := s.executeAction(ctx, ref, rp, input, rec)
	if errors.Is(runErr, errAuditUnavailable) {
		return model.ApprovalInfo{}, runErr
	}
	ran = true
	return s.approvals.Approve(req.ID, by, s.approvalOutcome(req, out, runErr))
}

// finishAfterPanic ends a request whose action ran before dash panicked: approved, its outcome a
// failure that says so. Should even that fail, the request stays claimed (no decision, no run)
// until a restart forgets it; it is never released to run again.
func (s *Server) finishAfterPanic(req approvals.Request, by string) (info model.ApprovalInfo, err error) {
	defer func() {
		if recover() != nil {
			info, err = model.ApprovalInfo{}, errAfterRun
		}
	}()
	info, err = s.approvals.Approve(req.ID, by, model.ApprovalOutcome{
		OK: false, Error: errAfterRun.Error() + ": its answer could not be kept", At: s.now().UTC(),
	})
	if err != nil {
		return model.ApprovalInfo{}, errAfterRun
	}
	return info, errAfterRun
}

// approvalOutcome is what an approved run is kept as: the answer as JSON (202's
// {"status":"accepted"} for none), redacted and cut to 64 KiB, or the error a person may read:
// a plugin's refusal as it worded it, any other failure by its category only.
func (s *Server) approvalOutcome(req approvals.Request, out any, err error) model.ApprovalOutcome {
	o := model.ApprovalOutcome{OK: err == nil, At: s.now().UTC()}
	if err != nil {
		var ae *plugin.ActionError
		if errors.As(err, &ae) {
			o.Error = s.d.Secrets.Scrub(ae.Message)
		} else {
			slog.Warn("approved action failed", "plugin", req.Plugin, "action", req.Action, "approval", req.ID,
				"category", errCategory(err), "error", s.d.Secrets.Scrub(err.Error()))
			o.Error = req.Plugin + ": " + errCategory(err)
		}
		return o
	}
	if out == nil {
		out = map[string]string{"status": "accepted"}
	}
	b, mErr := json.Marshal(out)
	if mErr != nil {
		o.OK, o.Error = false, "the action's answer could not be encoded"
		return o
	}
	o.Result, o.Truncated = s.d.Secrets.Cut(s.d.Secrets.Scrub(string(b)), model.ApprovalResultMaxBytes)
	return o
}

// canonicalInput is the input's JSON with its keys sorted and without confirm, and the hex
// SHA-256 of it: what the request shows, what dedup and the audit compare.
func canonicalInput(input any) (json.RawMessage, string, error) {
	b, err := json.Marshal(input)
	if err != nil {
		return nil, "", err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) == nil && fields != nil {
		for k := range fields {
			if strings.EqualFold(k, "confirm") {
				delete(fields, k)
			}
		}
		if b, err = json.Marshal(fields); err != nil {
			return nil, "", err
		}
	}
	sum := sha256.Sum256(b)
	return b, hex.EncodeToString(sum[:]), nil
}

// setConfirm sets the input's confirm field (its JSON name, matched as encoding/json does, in the
// struct or one it embeds) to v. An input without one is left alone.
func setConfirm(input any, v string) {
	if f := confirmField(input); f.IsValid() {
		f.SetString(v)
	}
}

func confirmField(input any) reflect.Value {
	v := reflect.ValueOf(input)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || !v.CanSet() {
		return reflect.Value{}
	}
	return jsonStringField(v, "confirm")
}

// jsonStringField is the settable string field of struct v whose JSON name is name, looking into
// embedded structs.
func jsonStringField(v reflect.Value, name string) reflect.Value {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if tag == "-" {
			continue
		}
		if f.Anonymous && tag == "" {
			fv := v.Field(i)
			if fv.Kind() == reflect.Pointer {
				if fv.IsNil() {
					continue
				}
				fv = fv.Elem()
			}
			if fv.Kind() == reflect.Struct {
				if got := jsonStringField(fv, name); got.IsValid() {
					return got
				}
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if strings.EqualFold(cmp.Or(tag, f.Name), name) && f.Type.Kind() == reflect.String {
			return v.Field(i)
		}
	}
	return reflect.Value{}
}
