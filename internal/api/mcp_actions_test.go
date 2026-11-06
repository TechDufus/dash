package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// hostBearer sends every request to dash's host with a bearer token.
type hostBearer struct{ token string }

func (h hostBearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = testHost
	r.Header.Set("Authorization", "Bearer "+h.token)
	return http.DefaultTransport.RoundTrip(r)
}

// mcpSession connects the official Go SDK client, named Test Agent, to the env's /mcp with token
// at protocol version.
func (e *approvalEnv) mcpSession(t *testing.T, token, version string, opts *sdk.ClientOptions) *sdk.ClientSession {
	t.Helper()
	srv := httptest.NewServer(e.Handler())
	t.Cleanup(srv.Close)
	tr := &sdk.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: hostBearer{token}}, DisableStandaloneSSE: true}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "Test Agent", Version: "1"}, opts).Connect(ctx, tr, &sdk.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func listTools(t *testing.T, cs *sdk.ClientSession) map[string]*sdk.Tool {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*sdk.Tool{}
	for _, tl := range res.Tools {
		out[tl.Name] = tl
	}
	return out
}

func call(t *testing.T, cs *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func structured[T any](t *testing.T, res *sdk.CallToolResult) T {
	t.Helper()
	var out T
	raw, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
	return out
}

func resultText(res *sdk.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	return res.Content[0].(*sdk.TextContent).Text
}

var restartArgs = map[string]any{"namespace": "apps", "name": "web", "kind": "Deployment"}

// The listing rule (contract D48): an administrator's mcp token holding actions:aa sees one tool
// per action, with its annotations and an input schema without confirm, and dash_approval;
// without the scope, as a viewer, or with the policy deny it sees no action tool, and calling
// one anyway is an unknown tool.
func TestMCPActionToolsListing(t *testing.T) {
	e := newApprovalEnv(t, nil)
	admin, _ := e.agentToken(t, "alice", "read", "actions:aa")
	tools := listTools(t, e.mcpSession(t, admin, "2026-07-28", nil))
	for _, name := range []string{"aa_restart", "aa_scale", "aa_ping", "aa_wipe", "dash_approval"} {
		if tools[name] == nil {
			t.Fatalf("%s is not listed: %v", name, slices.Collect(func(yield func(string) bool) {
				for n := range tools {
					yield(n)
				}
			}))
		}
	}
	restart := tools["aa_restart"]
	if a := restart.Annotations; a == nil || a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint || a.IdempotentHint ||
		a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v", restart.Annotations)
	}
	if a := tools["aa_ping"].Annotations; !a.IdempotentHint || *a.DestructiveHint {
		t.Errorf("ping annotations = %+v", a)
	}
	schema, _ := json.Marshal(restart.InputSchema)
	if strings.Contains(string(schema), "confirm") || !strings.Contains(string(schema), `"namespace"`) {
		t.Errorf("schema = %s", schema)
	}

	for name, tc := range map[string]struct {
		user   string
		scopes []string
	}{
		"no actions scope": {"alice", []string{"read"}},
		"a viewer":         {"bob", []string{"read", "actions:aa"}},
	} {
		token, _ := e.agentToken(t, tc.user, tc.scopes...)
		cs := e.mcpSession(t, token, "2026-07-28", nil)
		if got := listTools(t, cs); got["aa_restart"] != nil || got["aa_ping"] != nil {
			t.Errorf("%s sees action tools", name)
		}
		if _, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "aa_restart", Arguments: restartArgs}); err == nil || !strings.Contains(err.Error(), "unknown tool") {
			t.Errorf("%s calls it: %v", name, err)
		}
	}

	e.reconfigure(t, plugin.InstanceConfig{Name: "aa", ActionsEnabled: true, ActionsAgents: pluginconf.AgentsDeny})
	cs := e.mcpSession(t, admin, "2026-07-28", nil)
	if got := listTools(t, cs); got["aa_restart"] != nil || got["dash_approval"] == nil {
		t.Errorf("deny lists the action tools")
	}
	if _, err := cs.CallTool(context.Background(), &sdk.CallToolParams{Name: "aa_restart", Arguments: restartArgs}); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("deny: call = %v", err)
	}
	if n := len(e.acts()); n != 0 {
		t.Errorf("Act ran %d times", n)
	}
	// An api-audience principal never sees an action tool, whatever its scopes.
	api := auth.Principal{Kind: auth.KindToken, User: "alice", Role: auth.RoleAdmin, Audience: model.TokenAudienceAPI, Scopes: []string{"actions:aa"}}
	e.reconfigure(t, plugin.InstanceConfig{Name: "aa", ActionsEnabled: true})
	if (mcpBackend{e.Server}).ActionVisible(api, "aa_restart") {
		t.Error("an api token sees aa_restart")
	}
}

// approve with a client without URL elicitation (contract D53): the call answers the
// pending_approval JSON, the action does not run, the owner approves it through the API, the
// action runs once, and dash_approval returns the outcome.
func TestMCPActionPendingThenPolled(t *testing.T) {
	e := newApprovalEnv(t, func(string, any) (any, error) { return map[string]string{"restarted": "web"}, nil })
	token, p := e.agentToken(t, "alice", "read", "actions:aa")
	cs := e.mcpSession(t, token, "2025-11-25", nil)
	res := call(t, cs, "aa_restart", map[string]any{"namespace": "apps", "name": "web", "kind": "Deployment", "confirm": "web"})
	if res.IsError {
		t.Fatalf("call = %s", resultText(res))
	}
	pending := structured[model.ApprovalPending](t, res)
	if pending.Status != model.ApprovalStatusPending || pending.ApproveURL != testOrigin+"/approve/"+pending.ApprovalID || !strings.Contains(resultText(res), "dash_approval") {
		t.Fatalf("pending = %+v / %s", pending, resultText(res))
	}
	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times before the approval", n)
	}
	info := structured[model.ApprovalInfo](t, call(t, cs, "dash_approval", map[string]any{"id": pending.ApprovalID}))
	// A stateless 2025-11-25 call carries no clientInfo: the request names the token only.
	if info.State != model.ApprovalStatePending || info.ClientName != "" || info.TokenID != p.TokenID || info.CredentialName != "agent-alice" {
		t.Fatalf("dash_approval = %+v", info)
	}
	approvalOf(t, e.decide("alice", pending.ApprovalID, model.ApprovalDecision{Approve: true}))
	info = structured[model.ApprovalInfo](t, call(t, cs, "dash_approval", map[string]any{"id": pending.ApprovalID}))
	if info.State != model.ApprovalStateApproved || info.Outcome == nil || info.Outcome.Result != `{"restarted":"web"}` {
		t.Fatalf("dash_approval after approval = %+v", info)
	}
	if calls := e.acts(); len(calls) != 1 || calls[0].input.(*restartIn).Confirm != "" {
		t.Errorf("Act = %+v", calls)
	}
	// Another user's token cannot read it; a forgotten id is approval_expired.
	other, _ := e.agentToken(t, "carol", "read", "actions:aa")
	if res := call(t, e.mcpSession(t, other, "2025-11-25", nil), "dash_approval", map[string]any{"id": pending.ApprovalID}); !res.IsError || !strings.Contains(resultText(res), model.ErrCodeForbidden) {
		t.Errorf("carol's dash_approval = %s", resultText(res))
	}
	if res := call(t, cs, "dash_approval", map[string]any{"id": "appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ"}); !res.IsError || !strings.Contains(resultText(res), model.ErrCodeApprovalExpired) {
		t.Errorf("unknown dash_approval = %s", resultText(res))
	}
	recs := auditRecords(t, e.dir)
	if !slices.ContainsFunc(recs, func(r audit.Record) bool { return r.Action == auditApprovalResult && r.Approval == pending.ApprovalID }) {
		t.Error("the fetch of the result is not recorded")
	}
}

// A client that declares URL elicitation on 2026-07-28 gets an input-required answer pointing at
// the approval page; once the owner approved, its retry with the requestState answers the
// outcome (contract D53).
func TestMCPActionMultiRoundTrip(t *testing.T) {
	e := newApprovalEnv(t, nil)
	token, _ := e.agentToken(t, "alice", "read", "actions:aa")
	var urls []string
	cs := e.mcpSession(t, token, "2026-07-28", &sdk.ClientOptions{
		Capabilities: &sdk.ClientCapabilities{Elicitation: &sdk.ElicitationCapabilities{URL: &sdk.URLElicitationCapabilities{}}},
		ElicitationHandler: func(_ context.Context, req *sdk.ElicitRequest) (*sdk.ElicitResult, error) {
			urls = append(urls, req.Params.URL)
			id := req.Params.URL[strings.LastIndex(req.Params.URL, "/")+1:]
			if w := e.decide("alice", id, model.ApprovalDecision{Approve: true}); w.Code != http.StatusOK {
				t.Errorf("decision = %d %s", w.Code, w.Body)
			}
			return &sdk.ElicitResult{Action: "accept"}, nil
		},
	})
	res := call(t, cs, "aa_restart", restartArgs)
	if res.IsError || !strings.Contains(resultText(res), "Approved by alice and done.") || !strings.Contains(resultText(res), "accepted") {
		t.Fatalf("call = %v %s", res.IsError, resultText(res))
	}
	if len(urls) != 1 || !strings.HasPrefix(urls[0], testOrigin+"/approve/appr_") {
		t.Errorf("elicited %v", urls)
	}
	if n := len(e.acts()); n != 1 {
		t.Errorf("Act ran %d times", n)
	}
	// On 2026-07-28 every request carries the client's name, and the request records it.
	id := urls[0][strings.LastIndex(urls[0], "/")+1:]
	if info, err := e.approvals.Get(id, "alice"); err != nil || info.ClientName != "Test Agent" {
		t.Errorf("request = %+v, %v", info, err)
	}
}

// allow runs an action at once (the agent's confirm dropped), except on a target whose rule is
// typeName, which still waits for a person (contract D33, D49).
func TestMCPActionAllowRunsAtOnceExceptProtected(t *testing.T) {
	e := newApprovalEnv(t, nil, plugin.InstanceConfig{Name: "aa", ActionsEnabled: true, ActionsAgents: pluginconf.AgentsAllow})
	token, p := e.agentToken(t, "alice", "read", "actions:aa")
	cs := e.mcpSession(t, token, "2025-11-25", nil)
	res := call(t, cs, "aa_restart", map[string]any{"namespace": "apps", "name": "web", "confirm": "web"})
	if res.IsError || !strings.Contains(resultText(res), "accepted") {
		t.Fatalf("allow = %s", resultText(res))
	}
	calls := e.acts()
	if len(calls) != 1 || calls[0].input.(*restartIn).Confirm != "" || calls[0].p.TokenID != p.TokenID {
		t.Fatalf("Act = %+v", calls)
	}
	res = call(t, cs, "aa_wipe", map[string]any{"namespace": "vault", "name": "db", "confirm": "db"})
	if pending := structured[model.ApprovalPending](t, res); res.IsError || pending.Status != model.ApprovalStatusPending {
		t.Fatalf("protected under allow = %s", resultText(res))
	}
	if n := len(e.acts()); n != 1 {
		t.Errorf("the protected target ran: Act %d times", n)
	}
	// The run is audited as an action of the token on the mcp surface, with the input's hash.
	recs := auditRecords(t, e.dir)
	if !slices.ContainsFunc(recs, func(r audit.Record) bool {
		return r.Action == "restart" && r.Kind == "outcome" && r.Actor.TokenID == p.TokenID && r.Actor.Surface == "mcp" && len(r.ArgsSHA256) == 64
	}) {
		t.Errorf("no outcome record of the run: %+v", recs)
	}
}

// A token makes at most 10 action calls a minute; the 11th is rate_limited and creates nothing.
func TestMCPActionCallsAreLimited(t *testing.T) {
	e := newApprovalEnv(t, nil)
	token, _ := e.agentToken(t, "alice", "read", "actions:aa")
	cs := e.mcpSession(t, token, "2025-11-25", nil)
	for i := range 10 {
		if res := call(t, cs, "aa_restart", map[string]any{"namespace": "apps", "name": "web-" + string(rune('a'+i))}); res.IsError {
			t.Fatalf("call %d = %s", i+1, resultText(res))
		}
	}
	res := call(t, cs, "aa_restart", map[string]any{"namespace": "apps", "name": "one-more"})
	if !res.IsError || !strings.Contains(resultText(res), model.ErrCodeRateLimited) {
		t.Fatalf("11th = %s", resultText(res))
	}
	if n := len(e.approvals.Pending("alice")); n != 10 {
		t.Errorf("%d pending requests, want 10", n)
	}
	// The read tools keep their own rate.
	if res := call(t, cs, "dash_approval", map[string]any{"id": e.approvals.Pending("alice")[0].ID}); res.IsError {
		t.Errorf("dash_approval = %s", resultText(res))
	}
}
