package mcp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/bundle"
	"github.com/TechDufus/dash/internal/model"
)

// The action tools (design N.2, O; contract D48–D53): one tool per plugin action, named
// <plugin>_<action with . replaced by _>, listed and callable only for an administrator's token
// holding actions:<plugin> while the action is offered and its agent policy is not deny. A call
// runs at once under allow, or becomes an approval request its owner decides in dash.

// Limits of the action tools.
const (
	// actionsPerMinute is how many action calls one token makes a minute (design N.6), counted
	// when the call is made; the retries that wait for a decision do not count.
	actionsPerMinute = 10
	// approvalWait is how long a retry of an input-required answer waits for the decision
	// before it answers input-required again (contract D53).
	approvalWait = 30 * time.Second
)

// ActionTool is one action tool as the backend describes it.
type ActionTool struct {
	Name        string // <plugin>_<action with . -> _>
	Title       string
	Description string
	// InputSchema is the action input's JSON Schema without confirm: dash fills confirm only
	// from a person's decision (contract D49).
	InputSchema json.RawMessage
	Destructive bool
	Idempotent  bool
}

// ActionReply is the backend's answer to an action call: it ran (allow), or it waits for its
// owner's approval at ApproveURL.
type ActionReply struct {
	Ran        bool
	Result     any // the action's answer when it ran
	Approval   model.ApprovalInfo
	ApproveURL string
}

// ActionBackend is the part of the backend the action tools need; a Backend without it serves
// the read tools only.
type ActionBackend interface {
	// ActionTools are every action a registered plugin declares, in registry order.
	ActionTools() []ActionTool
	// ActionVisible reports whether p may see and call tool now (contract D48).
	ActionVisible(p auth.Principal, tool string) bool
	// CallAction checks and runs, or turns into an approval request, p's call of tool with
	// args, through client. A refusal is an *Error.
	CallAction(r *http.Request, p auth.Principal, client, tool string, args json.RawMessage) (ActionReply, error)
	// Approval is request id as p, its owner, reads it; a refusal is an *Error
	// (approval_expired for one dash does not know).
	Approval(r *http.Request, p auth.Principal, id string) (model.ApprovalInfo, error)
	// ApproveURL is the page where id is decided.
	ApproveURL(id string) string
	// WaitApproval blocks until request id is decided or expires, or ctx ends.
	WaitApproval(ctx context.Context, id string) bool
}

// actionBackend is the server's ActionBackend, nil when its Backend has none.
func (s *Server) actionBackend() ActionBackend {
	ab, _ := s.o.Backend.(ActionBackend)
	return ab
}

// addActionTools registers every action tool with the SDK; scopeFilter decides per request who
// sees and calls one.
func (s *Server) addActionTools() {
	ab := s.actionBackend()
	if ab == nil {
		return
	}
	for _, at := range ab.ActionTools() {
		schema := at.InputSchema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
		}
		destructive := at.Destructive
		s.actionTools = append(s.actionTools, at)
		s.srv.AddTool(&sdk.Tool{
			Name: at.Name, Title: at.Title, Description: at.Description, InputSchema: schema,
			Annotations: &sdk.ToolAnnotations{
				ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: at.Idempotent, OpenWorldHint: new(false), Title: at.Title,
			},
		}, s.actionHandler(ab, at.Name))
	}
}

// isActionTool reports whether name is one of the action tools.
func (s *Server) isActionTool(name string) bool {
	return slices.ContainsFunc(s.actionTools, func(at ActionTool) bool { return at.Name == name })
}

// actionHandler serves calls of one action tool. A call without requestState is a new call: it
// counts against the token's action rate, then runs or becomes a request. A retry with the
// requestState of an input-required answer waits for that request's decision.
func (s *Server) actionHandler(ab ActionBackend, tool string) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		c, ok := callerOf(req.Extra)
		if !ok {
			return nil, errors.New("no principal")
		}
		if st := req.Params.RequestState; st != "" {
			id, ok := s.openState(st, c.p, tool)
			if !ok {
				return errorResult(model.ErrCodeApprovalExpired, "this approval request expired or is unknown; call the tool again"), nil
			}
			return s.awaitApproval(ctx, ab, req, c, tool, id), nil
		}
		if !s.actionCalls.allow(tokenKey(c.p)) {
			return errorResult(model.ErrCodeRateLimited, "too many action calls for this token (10 a minute); try again in a minute"), nil
		}
		client := ""
		if ci := req.ClientInfo(); ci != nil {
			client = ci.Name
		}
		reply, err := ab.CallAction(c.r, c.p, client, tool, req.Params.Arguments)
		if te, ok := errors.AsType[*Error](err); ok {
			return errorResult(te.Code, s.o.Scrub(te.Message)), nil
		}
		if err != nil {
			s.o.Log.Warn("mcp action failed", "tool", tool, "err", s.o.Scrub(err.Error()))
			return errorResult(model.ErrCodeUpstreamUnavailable, "the action failed upstream; try again later"), nil
		}
		if reply.Ran {
			return s.ranResult("Done.", reply.Result)
		}
		return s.pendingResult(req, c, tool, reply.Approval, reply.ApproveURL)
	}
}

// ranResult is the answer of an action that ran: dash's line, then its answer as lab data.
func (s *Server) ranResult(head string, out any) (*sdk.CallToolResult, error) {
	if out == nil {
		out = map[string]string{"status": "accepted"}
	}
	tree, err := bundle.Scrub(out, s.o.Scrub)
	if err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(tree, "", "  ")
	if err != nil {
		return nil, err
	}
	head += "\n"
	res := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: head + bundle.UntrustedBlock(string(raw))}}, StructuredContent: tree}
	return s.capResult(res, tree, head, string(raw)), nil
}

// wantsURL reports whether the client takes a URL elicitation as a multi-round-trip input
// request: it speaks 2026-07-28 and declares URL-mode elicitation.
func wantsURL(req *sdk.CallToolRequest) bool {
	if v, _ := req.Params.GetMeta()[sdk.MetaKeyProtocolVersion].(string); v < ProtocolVersions[0] {
		return false
	}
	caps := req.ClientCapabilities()
	return caps != nil && caps.Elicitation != nil && caps.Elicitation.URL != nil
}

// pendingResult answers a call whose request waits for its owner (contract D53): an
// input-required result pointing at the approval page for a client that takes a URL
// elicitation, else the pending_approval JSON the agent polls dash_approval with.
func (s *Server) pendingResult(req *sdk.CallToolRequest, c caller, tool string, info model.ApprovalInfo, approveURL string) (*sdk.CallToolResult, error) {
	if wantsURL(req) {
		return s.inputRequired(c, tool, info, approveURL), nil
	}
	pending := model.ApprovalPending{Status: model.ApprovalStatusPending, ApprovalID: info.ID, ApproveURL: approveURL, ExpiresAt: info.ExpiresAt}
	expires := info.ExpiresAt.UTC().Format(time.RFC3339)
	text := fmt.Sprintf("This action waits for its owner's approval in dash. Give the user this link to approve it: %s. "+
		"It expires at %s. Do not call the tool again: call dash_approval with {\"id\": %q} to learn the decision and the result.",
		approveURL, expires, info.ID)
	pending.ExpiresAt = info.ExpiresAt.UTC()
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}, StructuredContent: pending}, nil
}

// inputRequired is the multi-round-trip answer: open the approval page, then retry with the
// requestState, which names the request for this token and tool until it expires.
func (s *Server) inputRequired(c caller, tool string, info model.ApprovalInfo, approveURL string) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		InputRequests: sdk.InputRequestMap{"approve": &sdk.ElicitParams{
			Mode: "url", URL: approveURL, ElicitationID: info.ID,
			Message: "dash needs a person to approve this action, before " + info.ExpiresAt.UTC().Format(time.RFC3339) + ": " + info.Summary,
		}},
		RequestState: s.sealState(info.ID, c.p, tool, info.ExpiresAt),
	}
}

// awaitApproval answers a retry: it waits up to approvalWait (no longer than the request lives)
// for the decision, then answers the outcome, or input-required again while it still waits.
func (s *Server) awaitApproval(ctx context.Context, ab ActionBackend, req *sdk.CallToolRequest, c caller, tool, id string) *sdk.CallToolResult {
	info, err := ab.Approval(c.r, c.p, id)
	if err == nil && info.State == model.ApprovalStatePending {
		wait := min(approvalWait, max(info.ExpiresAt.Sub(s.o.Now()), 0))
		wctx, cancel := context.WithTimeout(ctx, wait)
		ab.WaitApproval(wctx, id)
		cancel()
		info, err = ab.Approval(c.r, c.p, id)
	}
	if te, ok := errors.AsType[*Error](err); ok {
		return errorResult(te.Code, s.o.Scrub(te.Message))
	}
	if err != nil {
		return errorResult(model.ErrCodeUpstreamUnavailable, "the approval cannot be read right now")
	}
	if info.State == model.ApprovalStatePending {
		return s.inputRequired(c, tool, info, ab.ApproveURL(id))
	}
	res, rerr := s.decidedResult(info)
	if rerr != nil {
		return errorResult(model.ErrCodeInternal, "internal error")
	}
	return res
}

// decidedResult is a decided or expired request as an action tool answers it: the outcome of an
// approved one, approval_denied or approval_expired otherwise.
func (s *Server) decidedResult(info model.ApprovalInfo) (*sdk.CallToolResult, error) {
	switch info.State {
	case model.ApprovalStateDenied:
		msg := "the owner denied this action"
		if info.Reason != "" {
			msg = "dash did not run it: " + info.Reason
		}
		return errorResult(model.ErrCodeApprovalDenied, msg), nil
	case model.ApprovalStateExpired:
		return errorResult(model.ErrCodeApprovalExpired, "nobody approved this action in time"), nil
	}
	o := info.Outcome
	if o == nil {
		return errorResult(model.ErrCodeInternal, "the approved action has no outcome"), nil
	}
	if !o.OK {
		return errorResult(model.ErrCodeUpstreamUnavailable, "approved by "+info.DecidedBy+", but it failed: "+o.Error), nil
	}
	var out any = o.Result
	if json.Valid([]byte(o.Result)) {
		out = json.RawMessage(o.Result)
	}
	return s.ranResult("Approved by "+info.DecidedBy+" and done.", out)
}

// approvalInput is dash_approval's input.
type approvalInput struct {
	ID string `json:"id" dash:"required" doc:"The approvalId an action tool answered."`
}

// approvalView is dash_approval's answer: the request, its outcome's result decoded as JSON when
// it parses (it is the action's answer), else kept as text.
type approvalView struct {
	model.ApprovalInfo
	Outcome *outcomeView `json:"outcome,omitempty"`
}

type outcomeView struct {
	OK        bool      `json:"ok"`
	Result    any       `json:"result"`
	Truncated bool      `json:"truncated"`
	Error     string    `json:"error"`
	At        time.Time `json:"at"`
}

func viewOf(info model.ApprovalInfo) approvalView {
	v := approvalView{ApprovalInfo: info}
	if o := info.Outcome; o != nil {
		var result any = o.Result
		if o.Result != "" && json.Valid([]byte(o.Result)) {
			result = json.RawMessage(o.Result)
		}
		v.Outcome = &outcomeView{OK: o.OK, Result: result, Truncated: o.Truncated, Error: o.Error, At: o.At.UTC()}
	}
	return v
}

// approvalHead is dash's own line ahead of dash_approval's data: the state, who decided, and how
// the action ran.
func approvalHead(v any) string {
	info := v.(approvalView).ApprovalInfo
	line := "Approval " + info.ID + " (" + info.Tool + "): " + info.State
	switch {
	case info.State == model.ApprovalStatePending:
		line += ", waiting for its owner until " + info.ExpiresAt.UTC().Format(time.RFC3339)
	case info.DecidedBy != "":
		line += " by " + info.DecidedBy
	case info.Reason != "":
		line += ": " + info.Reason
	}
	if o := info.Outcome; o != nil {
		if o.OK {
			line += "; the action succeeded"
		} else {
			line += "; the action failed (outcome.error says why)"
		}
	}
	return line + ".\n"
}

// approvalTool is dash_approval (design N.2): an approval request's state, and the action's
// result once it ran.
func (s *Server) approvalTool(ab ActionBackend) tool {
	return tool{
		name: "dash_approval", title: "Approval state", scope: "read", input: &approvalInput{},
		desc: "The state of an approval request an action tool made: pending, approved, denied or expired, with the " +
			"action's outcome once it ran. Poll it, at most every few seconds, until it is no longer pending.",
		run: func(_ context.Context, c caller, in any) (any, string, error) {
			info, err := ab.Approval(c.r, c.p, in.(*approvalInput).ID)
			if err != nil {
				return nil, "", err
			}
			return viewOf(info), "", nil
		},
		head: approvalHead,
	}
}

// The requestState of an input-required answer: the request's id, the token, the tool and when
// the request expires, sealed with a key this process made (contract D53). A state from another
// process, another token or another tool does not open.
type requestState struct {
	ID      string `json:"i"`
	Token   string `json:"k"`
	Tool    string `json:"t"`
	Expires int64  `json:"e"`
}

var b64 = base64.RawURLEncoding

func newStateKey() []byte {
	k := make([]byte, 32)
	_, _ = rand.Read(k) // never fails since Go 1.24
	return k
}

func (s *Server) sealState(id string, p auth.Principal, tool string, expires time.Time) string {
	payload, _ := json.Marshal(requestState{ID: id, Token: tokenKey(p), Tool: tool, Expires: expires.Unix()})
	mac := hmac.New(sha256.New, s.stateKey)
	mac.Write(payload)
	return b64.EncodeToString(payload) + "." + b64.EncodeToString(mac.Sum(nil))
}

func (s *Server) openState(state string, p auth.Principal, tool string) (string, bool) {
	enc, sig, ok := strings.Cut(state, ".")
	if !ok {
		return "", false
	}
	payload, err1 := b64.DecodeString(enc)
	got, err2 := b64.DecodeString(sig)
	if err1 != nil || err2 != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, s.stateKey)
	mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return "", false
	}
	var st requestState
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if dec.Decode(&st) != nil || st.Token != tokenKey(p) || st.Tool != tool || s.o.Now().Unix() >= st.Expires {
		return "", false
	}
	return st.ID, true
}
