package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/tokens"
)

// approvalEnv is the action fixture "aa" served by a proxy-mode server (alice and carol are the
// administrators, bob a viewer) with a credential store, the real token resolver, MCP on and a
// real audit log. Its extra action "wipe" needs the target's name typed in the namespace vault.
type approvalEnv struct {
	*actionEnv
	store *credentials.Store
}

// protectedLater makes the namespace "later" need the typed name from now on (a namespace newly
// protected); fragilePanics makes fragileIn's Validate panic. The tests that set them reset them.
var (
	protectedLater atomic.Bool
	fragilePanics  atomic.Bool
)

var wipeSpec = plugin.ActionSpec{
	Name: "wipe", Title: "Wipe", Destructive: true, Confirm: plugin.ConfirmDialog,
	Input:   func() any { return &restartIn{} },
	Summary: func(in any) string { i := in.(*restartIn); return "Wipe " + i.Namespace + "/" + i.Name },
	Target:  func(in any) (string, string, string) { i := in.(*restartIn); return "Workload", i.Namespace, i.Name },
	ConfirmFor: func(_ context.Context, in any) plugin.Confirm {
		if ns := in.(*restartIn).Namespace; ns == "vault" || ns == "later" && protectedLater.Load() {
			return plugin.ConfirmTypeName
		}
		return plugin.ConfirmDialog
	},
}

// fragileIn is the input of "fragile", whose Validate panics while fragilePanics is set.
type fragileIn struct {
	Name string `json:"name" dash:"required"`
}

func (f *fragileIn) Validate() error {
	if fragilePanics.Load() {
		panic("fragile input")
	}
	return nil
}

var fragileSpec = plugin.ActionSpec{
	Name: "fragile", Title: "Fragile", Input: func() any { return &fragileIn{} },
	Target: func(in any) (string, string, string) { return "Thing", "", in.(*fragileIn).Name },
}

func newApprovalEnv(t *testing.T, fn func(action string, in any) (any, error), ics ...plugin.InstanceConfig) *approvalEnv {
	t.Helper()
	if len(ics) == 0 {
		ics = []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: true}}
	}
	e := &approvalEnv{actionEnv: newActionEnvSpecs(t, fn, ics, []plugin.ActionSpec{wipeSpec, fragileSpec})}
	store, err := credentials.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	e.store = store
	roles := auth.NewRoleHolder(auth.RoleMap{AdminUsers: []string{"alice", "carol"}})
	e.dir = t.TempDir()
	e.Server = newServer(func(d *Deps) {
		d.Hub, d.Plugins, d.Audit = e.hub, e.sup, openAudit(t, e.dir, e.clk.now)
		d.Credentials, d.Tokens = store, tokens.New(store, roles, nil, e.clk.now)
		d.MCPEnabled = func() bool { return true }
	})
	e.Server.now = e.clk.now
	return e
}

// agent is the principal of a new mcp personal access token of user holding scopes.
func (e *approvalEnv) agent(t *testing.T, user string, scopes ...string) auth.Principal {
	t.Helper()
	_, p := e.agentToken(t, user, scopes...)
	return p
}

// agentToken is a new mcp personal access token of user holding scopes, and its principal.
func (e *approvalEnv) agentToken(t *testing.T, user string, scopes ...string) (string, auth.Principal) {
	t.Helper()
	token, id, hash, err := credentials.NewPAT()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.store.AddPAT(credentials.PAT{
		ID: id, Name: "agent-" + user, User: user, Hash: hash, Audience: model.TokenAudienceMCP, Scopes: scopes,
		CreatedAt: fixedNow, ExpiresAt: fixedNow.Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	p, err := e.d.Tokens.Resolve(token, model.TokenAudienceMCP)
	if err != nil {
		t.Fatal(err)
	}
	return token, p
}

// ask is the agent's call of action with args, checked and turned into a request.
func (e *approvalEnv) ask(t *testing.T, p auth.Principal, action string, args any) (model.ApprovalInfo, bool, *actionDenial) {
	t.Helper()
	ref, ok := e.sup.Action("aa", action)
	if !ok {
		t.Fatalf("action %s is not mounted", action)
	}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	c := agentCall{P: p, ClientName: "Test Agent", Ref: ref, Args: b, RequestID: "req-1", Peer: "127.0.0.1"}
	a, d := e.checkAgentAction(context.Background(), c)
	if d != nil {
		return model.ApprovalInfo{}, false, d
	}
	return e.requestApproval(c, a)
}

func (e *approvalEnv) mustAsk(t *testing.T, p auth.Principal, action string, args any) model.ApprovalInfo {
	t.Helper()
	info, _, d := e.ask(t, p, action, args)
	if d != nil {
		t.Fatalf("ask %s = %+v", action, d)
	}
	return info
}

func (e *approvalEnv) decide(user, id string, body any) *httptest.ResponseRecorder {
	return as(user, req{method: http.MethodPost, path: "/api/v1/approvals/" + id + "/decision", body: body}).do(e.Handler())
}

func (e *approvalEnv) get(user, id string) *httptest.ResponseRecorder {
	return as(user, req{path: "/api/v1/approvals/" + id}).do(e.Handler())
}

func approvalOf(t *testing.T, w *httptest.ResponseRecorder) model.ApprovalInfo {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var info model.ApprovalInfo
	decodeJSON(t, w, &info)
	return info
}

// pendingFor is the core.approvals payload the hub holds for user.
func (e *approvalEnv) pendingFor(t *testing.T, user string) []model.ApprovalInfo {
	t.Helper()
	ev, ok := e.hub.Latest(user, model.TopicApprovals)
	if !ok {
		return nil
	}
	if ev.User != user {
		t.Fatalf("%s's core.approvals is %s's", user, ev.User)
	}
	var env struct {
		Data model.ApprovalsData `json:"data"`
	}
	if err := json.Unmarshal(ev.Data, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data.Pending
}

// The whole life of an approved request: the agent's call creates it (the agent's own confirm
// dropped), core.approvals shows it to its owner only, the owner approves it once, the action runs
// once as the requester, the request keeps the outcome, and the audit log has the creation, the
// decision (intent, then outcome) and the run with the approval's id and approver, never an
// argument's value.
func TestApprovalLifecycle(t *testing.T) {
	e := newApprovalEnv(t, func(string, any) (any, error) { return map[string]string{"restarted": "web"}, nil })
	p := e.agent(t, "alice", "read", "actions:aa")
	info := e.mustAsk(t, p, "restart", map[string]string{"namespace": "apps", "name": "web", "kind": "Deployment", "confirm": "web"})
	if info.State != model.ApprovalStatePending || info.User != "alice" || info.TokenID != p.TokenID || info.CredentialName != "agent-alice" ||
		info.ClientName != "Test Agent" || info.Plugin != "aa" || info.Action != "restart" || info.Tool != "aa_restart" || !info.Destructive ||
		info.Target != (model.ApprovalTarget{Kind: "Workload", Namespace: "apps", Name: "web"}) || info.Confirm != plugin.ConfirmNone {
		t.Fatalf("info = %+v", info)
	}
	if string(info.Input) != `{"kind":"Deployment","name":"web","namespace":"apps"}` || len(info.InputSHA256) != 64 {
		t.Errorf("input = %s (%s)", info.Input, info.InputSHA256)
	}
	if got := e.pendingFor(t, "alice"); len(got) != 1 || got[0].ID != info.ID {
		t.Errorf("alice's core.approvals = %+v", got)
	}
	if got := e.pendingFor(t, "bob"); len(got) != 0 {
		t.Errorf("bob sees %+v", got)
	}
	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times before the decision", n)
	}
	if got := approvalOf(t, e.get("alice", info.ID)); got.ID != info.ID || got.State != model.ApprovalStatePending {
		t.Errorf("GET = %+v", got)
	}

	done := approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}))
	if done.State != model.ApprovalStateApproved || done.DecidedBy != "alice" || done.DecidedAt == nil || done.Outcome == nil ||
		!done.Outcome.OK || done.Outcome.Result != `{"restarted":"web"}` {
		t.Fatalf("decision = %+v %+v", done, done.Outcome)
	}
	calls := e.acts()
	if len(calls) != 1 {
		t.Fatalf("Act ran %d times", len(calls))
	}
	if in := calls[0].input.(*restartIn); *in != (restartIn{Namespace: "apps", Name: "web", Kind: "Deployment"}) {
		t.Errorf("Act got %+v: the agent's confirm must not reach it", in)
	}
	if c := calls[0].p; c.Kind != auth.KindToken || c.User != "alice" || c.TokenID != p.TokenID || c.Audience != model.TokenAudienceMCP {
		t.Errorf("Act ran as %+v, want the requester", c)
	}
	if got := e.pendingFor(t, "alice"); len(got) != 0 {
		t.Errorf("core.approvals after the decision = %+v", got)
	}
	wantError(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}), http.StatusConflict, model.ErrCodeConflict)
	if n := len(e.acts()); n != 1 {
		t.Errorf("a second decision ran Act again (%d)", n)
	}

	recs := auditRecords(t, e.dir)
	var seq []string
	for _, r := range recs {
		seq = append(seq, string(r.Kind)+" "+r.Action)
		if r.Approval != info.ID || r.ArgsSHA256 != info.InputSHA256 || r.Plugin != "aa" {
			t.Errorf("record %s %s lacks the approval: %+v", r.Kind, r.Action, r)
		}
	}
	want := []string{"event approval.create", "intent approval.approve", "intent restart", "outcome restart", "outcome approval.approve"}
	if !slices.Equal(seq, want) {
		t.Fatalf("records = %v, want %v", seq, want)
	}
	if a := recs[1].Actor; a.User != "alice" || a.Surface != "ui" {
		t.Errorf("the decision's actor = %+v, want alice in the UI", a)
	}
	if r := recs[2]; r.Actor.TokenID != p.TokenID || r.Actor.Surface != model.TokenAudienceMCP || r.ApprovedBy != "alice" || r.Client != "Test Agent" {
		t.Errorf("the run's record = %+v", r)
	}
	if recs[3].Result != audit.ResultOK || recs[4].Result != audit.ResultOK {
		t.Errorf("outcomes = %+v / %+v", recs[3], recs[4])
	}
	raw, err := os.ReadFile(filepath.Join(e.dir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "Deployment") || strings.Contains(string(raw), `"confirm"`) {
		t.Errorf("the audit log holds argument values: %s", raw)
	}
}

// A denial runs nothing and is final.
func TestApprovalDenied(t *testing.T) {
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	got := approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: false}))
	if got.State != model.ApprovalStateDenied || got.DecidedBy != "alice" || got.Outcome != nil {
		t.Errorf("denied = %+v", got)
	}
	wantError(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}), http.StatusConflict, model.ErrCodeConflict)
	if n := len(e.acts()); n != 0 {
		t.Errorf("Act ran %d times", n)
	}
}

// Only the owner reads or decides a request: another user, administrator or not, gets 403 and
// nothing runs; an id that is not one is 404, an unknown well-formed one 410.
func TestApprovalOfAnotherUserIs403(t *testing.T) {
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	for _, user := range []string{"bob", "carol"} {
		wantError(t, e.get(user, info.ID), http.StatusForbidden, model.ErrCodeForbidden)
		wantError(t, e.decide(user, info.ID, model.ApprovalDecision{Approve: true}), http.StatusForbidden, model.ErrCodeForbidden)
	}
	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times", n)
	}
	wantError(t, e.get("alice", "appr_nope"), http.StatusNotFound, model.ErrCodeNotFound)
	wantError(t, e.get("alice", "appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ"), http.StatusGone, model.ErrCodeApprovalExpired)
	wantError(t, e.decide("alice", "appr_ABCDEFGHIJKLMNOPQRSTUVWXYZ", model.ApprovalDecision{Approve: true}), http.StatusGone, model.ErrCodeApprovalExpired)
	if got := approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true})); got.State != model.ApprovalStateApproved {
		t.Errorf("the owner's decision = %+v", got)
	}
}

// A token reaches the routes only with the approvals scope (the dash-android grant); the
// anonymous viewer never.
func TestApprovalRoutesNeedThePersonOrTheApprovalsScope(t *testing.T) {
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	path := "/api/v1/approvals/" + info.ID
	for _, tc := range []struct {
		name string
		p    auth.Principal
		want int
	}{
		{"an api token with read", auth.Principal{Kind: auth.KindToken, User: "alice", Role: auth.RoleAdmin, Audience: "api", Scopes: []string{"read", "actions:aa"}}, http.StatusForbidden},
		{"a grant with approvals", auth.Principal{Kind: auth.KindToken, User: "alice", Role: auth.RoleAdmin, Audience: "api", GrantID: "g1", Scopes: []string{"read", model.OAuthScopeApprovals}}, http.StatusOK},
		{"the anonymous viewer", auth.Principal{Kind: auth.KindAnonymous, User: "anonymous", Role: auth.RoleViewer}, http.StatusForbidden},
	} {
		e.d.Auth = auth.Fixed(tc.p)
		if w := (req{path: path}).do(e.Handler()); w.Code != tc.want {
			t.Errorf("%s: GET = %d %s, want %d", tc.name, w.Code, w.Body, tc.want)
		}
	}
}

// A request expires 10 minutes after it was made: it leaves core.approvals, its expiry is
// recorded, and a decision is 410.
func TestApprovalExpiryIs410(t *testing.T) {
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	e.clk.advance(10 * time.Minute)
	e.approvals.Sweep()
	if got := e.pendingFor(t, "alice"); len(got) != 0 {
		t.Errorf("core.approvals after expiry = %+v", got)
	}
	wantError(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}), http.StatusGone, model.ErrCodeApprovalExpired)
	if n := len(e.acts()); n != 0 {
		t.Errorf("Act ran %d times", n)
	}
	recs := auditRecords(t, e.dir)
	if r := recs[len(recs)-1]; r.Kind != audit.KindEvent || r.Action != auditApprovalExpire || r.Approval != info.ID {
		t.Errorf("last record = %+v", r)
	}
}

// A target whose rule is typeName needs its name typed by the person approving (400 otherwise,
// and the request stays pending); that name, never the agent's, is the confirm Act receives.
func TestApprovalOfAProtectedTargetNeedsTheTypedName(t *testing.T) {
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "wipe", map[string]string{"namespace": "vault", "name": "db", "confirm": "db"})
	if info.Confirm != plugin.ConfirmTypeName {
		t.Fatalf("rule = %q", info.Confirm)
	}
	for _, confirm := range []string{"", "DB", "vault"} {
		got := wantError(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true, Confirm: confirm}), http.StatusBadRequest, model.ErrCodeInvalidInput)
		if got.Error != "type the Workload's name (db) to approve" {
			t.Errorf("message = %q", got.Error)
		}
	}
	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times without the typed name", n)
	}
	if got := approvalOf(t, e.get("alice", info.ID)); got.State != model.ApprovalStatePending {
		t.Fatalf("after the refusals: %+v", got)
	}
	if got := approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true, Confirm: "db"})); got.State != model.ApprovalStateApproved {
		t.Fatalf("with the name: %+v", got)
	}
	if calls := e.acts(); len(calls) != 1 || calls[0].input.(*restartIn).Confirm != "db" {
		t.Errorf("Act got %+v", calls)
	}
	// A deny needs no name.
	other := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "wipe", map[string]string{"namespace": "vault", "name": "cache"})
	if got := approvalOf(t, e.decide("alice", other.ID, model.ApprovalDecision{Approve: false})); got.State != model.ApprovalStateDenied {
		t.Errorf("deny = %+v", got)
	}
	// Elsewhere the agent's confirm is still dropped, and none is needed.
	plain := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "wipe", map[string]string{"namespace": "apps", "name": "web", "confirm": "web"})
	if plain.Confirm != plugin.ConfirmDialog {
		t.Fatalf("rule = %q", plain.Confirm)
	}
	approvalOf(t, e.decide("alice", plain.ID, model.ApprovalDecision{Approve: true}))
	if calls := e.acts(); len(calls) != 2 || calls[1].input.(*restartIn).Confirm != "" {
		t.Errorf("Act got %+v", calls)
	}
}

// Of concurrent approvals of one request exactly one runs the action; the others are 409.
func TestApprovalRunsExactlyOnce(t *testing.T) {
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	var wg sync.WaitGroup
	codes := make([]int, 16)
	for i := range codes {
		wg.Go(func() { codes[i] = e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}).Code })
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != len(codes)-1 {
		t.Errorf("codes = %v", codes)
	}
	if n := len(e.acts()); n != 1 {
		t.Errorf("Act ran %d times", n)
	}
}

// The decision is an audited write: when its intent cannot be written the answer is 503, nothing
// runs, and the request stays pending for a later decision, which then runs it once.
func TestApprovalAuditFailureIs503AndNothingRuns(t *testing.T) {
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	working := e.d.Audit
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	al, err := audit.Open(dir, audit.Options{MaxSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { al.Close() })
	al.Event(audit.Record{Action: "seed"})
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	e.d.Audit = al
	wantError(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}), http.StatusServiceUnavailable, model.ErrCodeAuditUnavailable)
	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times without a durable intent", n)
	}
	e.d.Audit = working
	if got := approvalOf(t, e.get("alice", info.ID)); got.State != model.ApprovalStatePending {
		t.Fatalf("after the failure: %+v", got)
	}
	if got := approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true})); got.State != model.ApprovalStateApproved {
		t.Fatalf("the later decision = %+v", got)
	}
	if n := len(e.acts()); n != 1 {
		t.Errorf("Act ran %d times", n)
	}
}

// An approval runs as the requester stands when it is approved: a revoked token, a lost scope, a
// lost administrator role, a namespace now denied, actions turned off or an agent policy now deny
// end the request denied with the reason, and nothing runs.
func TestApprovalRevalidatesTheRequesterAtExecution(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(t *testing.T, e *approvalEnv, p auth.Principal)
		reason string
	}{
		{"the token is revoked", func(t *testing.T, e *approvalEnv, p auth.Principal) {
			if _, _, err := e.store.RevokePAT(p.TokenID); err != nil {
				t.Fatal(err)
			}
		}, "revoked"},
		{"the owner is no longer an administrator", func(t *testing.T, e *approvalEnv, p auth.Principal) {
			e.d.Tokens = tokens.New(e.store, auth.NewRoleHolder(auth.RoleMap{AdminUsers: []string{"carol"}}), nil, e.clk.now)
		}, "administrator"},
		{"the namespace is now denied", func(t *testing.T, e *approvalEnv, p auth.Principal) {
			e.reconfigure(t, plugin.InstanceConfig{Name: "aa", ActionsEnabled: true, DenyNamespaces: []string{"apps"}})
		}, "namespace"},
		{"the agent policy is now deny", func(t *testing.T, e *approvalEnv, p auth.Principal) {
			e.reconfigure(t, plugin.InstanceConfig{Name: "aa", ActionsEnabled: true, ActionsAgents: pluginconf.AgentsDeny})
		}, "agent policy"},
		{"actions are turned off", func(t *testing.T, e *approvalEnv, p auth.Principal) {
			e.reconfigure(t, plugin.InstanceConfig{Name: "aa", ActionsEnabled: false})
		}, "no longer offered"},
		{"MCP is switched off", func(t *testing.T, e *approvalEnv, p auth.Principal) {
			e.d.MCPEnabled = func() bool { return false }
		}, "MCP is off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newApprovalEnv(t, nil)
			p := e.agent(t, "alice", "actions:aa")
			info := e.mustAsk(t, p, "restart", okRestart)
			tc.change(t, e, p)
			got := approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}))
			if got.State != model.ApprovalStateDenied || !strings.Contains(got.Reason, tc.reason) || got.DecidedBy != "" || got.Outcome != nil {
				t.Errorf("decision = %+v, want denied for %q", got, tc.reason)
			}
			if n := len(e.acts()); n != 0 {
				t.Errorf("Act ran %d times", n)
			}
		})
	}
}

// reconfigure applies ic and waits until the action ref reflects it.
func (e *approvalEnv) reconfigure(t *testing.T, ic plugin.InstanceConfig) {
	t.Helper()
	e.set.Set(ic)
	e.sup.Reload()
	e.eventually(t, "the new plugin settings", func() bool {
		ref, ok := e.sup.Action("aa", "restart")
		if !ic.ActionsEnabled {
			return !ok
		}
		return ok && slices.Equal(ref.DenyNamespaces, ic.DenyNamespaces) && (ic.ActionsAgents == "" || ref.Agents == ic.ActionsAgents)
	})
}

// The agent's call is checked before a request exists: an api-free check of the role, the scope,
// the input and the target, so a refused call never reaches a person.
func TestAgentCallIsCheckedBeforeARequest(t *testing.T) {
	e := newApprovalEnv(t, nil, plugin.InstanceConfig{Name: "aa", ActionsEnabled: true, DenyNamespaces: []string{"kube-system"}})
	admin := e.agent(t, "alice", "actions:aa")
	for _, tc := range []struct {
		name string
		p    auth.Principal
		args any
		code string
	}{
		{"a viewer's token", e.agent(t, "bob", "actions:aa"), okRestart, model.ErrCodeForbidden},
		{"no actions scope", e.agent(t, "alice", "read"), okRestart, model.ErrCodeInsufficientScope},
		{"an invalid input", admin, map[string]string{"namespace": "apps"}, model.ErrCodeInvalidInput},
		{"an unknown field", admin, map[string]string{"namespace": "apps", "name": "web", "x": "y"}, model.ErrCodeInvalidInput},
		{"a denied namespace", admin, restartIn{Namespace: "kube-system", Name: "dns"}, model.ErrCodeForbidden},
	} {
		if _, _, d := e.ask(t, tc.p, "restart", tc.args); d == nil || d.Code != tc.code {
			t.Errorf("%s: %+v, want %s", tc.name, d, tc.code)
		}
	}
	if got := e.approvals.Pending("alice"); len(got) != 0 {
		t.Errorf("pending = %+v", got)
	}
}

// The same call twice is one request; the 21st pending request of a user is 429.
func TestApprovalDedupAndTheTwentyFirst(t *testing.T) {
	e := newApprovalEnv(t, nil)
	p := e.agent(t, "alice", "actions:aa")
	first, created, _ := e.ask(t, p, "restart", okRestart)
	again, createdAgain, _ := e.ask(t, p, "restart", restartIn{Name: "web", Namespace: "apps", Kind: "Deployment", Confirm: "other"})
	if !created || createdAgain || again.ID != first.ID {
		t.Fatalf("dedup: %s %v / %s %v", first.ID, created, again.ID, createdAgain)
	}
	for i := 1; i < model.ApprovalsPendingMax; i++ {
		e.mustAsk(t, p, "restart", restartIn{Namespace: "apps", Name: "web-" + string(rune('a'+i))})
	}
	_, _, d := e.ask(t, p, "restart", restartIn{Namespace: "apps", Name: "one-too-many"})
	if d == nil || d.Status != http.StatusTooManyRequests || d.Code != model.ErrCodeRateLimited {
		t.Errorf("21st = %+v", d)
	}
}

// core.approvals reaches the owner's /api/v1/stream: in the snapshot on connect, then live as the
// request is decided. Another user's stream never carries it.
func TestCoreApprovalsReachesTheOwnersStream(t *testing.T) {
	e := newApprovalEnv(t, nil)
	e.heartbeat = time.Hour
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	h := e.Handler()
	next := func(lines <-chan string) (event, data string) {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case l := <-lines:
				if name, ok := strings.CutPrefix(l, "event: "); ok {
					event = name
				}
				if d, ok := strings.CutPrefix(l, "data: "); ok && event != "" {
					return event, d
				}
			case <-timeout:
				t.Fatal("timed out waiting for an event")
			}
		}
	}
	_, alice := sseClientWith(t, h, "/api/v1/stream?topics=core.approvals,test.marker", map[string]string{"X-Forwarded-Preferred-Username": "alice"})
	if ev, data := next(alice); ev != "core.approvals" || !strings.Contains(data, info.ID) {
		t.Fatalf("alice's first event = %s %s", ev, data)
	}
	_, bob := sseClientWith(t, h, "/api/v1/stream?topics=core.approvals,test.marker", map[string]string{"X-Forwarded-Preferred-Username": "bob"})

	approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: false}))
	if ev, data := next(alice); ev != "core.approvals" || strings.Contains(data, info.ID) || !strings.Contains(data, `"pending":[]`) {
		t.Errorf("alice's live event = %s %s", ev, data)
	}
	e.hub.Publish("test.marker", 1, nil)
	if ev, data := next(bob); ev != "test.marker" {
		t.Errorf("bob's first event = %s %s, want the marker", ev, data)
	}
}

// A new request is pushed once to each of its owner's verified devices that take approvals
// (contract D55): not to a device with approvals off, not to an unverified one, not to another
// user's, and not again for a duplicate call.
func TestApprovalIsPushedToVerifiedDevicesTakingApprovals(t *testing.T) {
	e := newPushEnv(t)
	verified := func(user, name string, approvals bool) pushDevice {
		t.Helper()
		d := newPushDevice(t, e.f)
		body := d.request(name)
		body.Notify.Approvals = approvals
		w := e.do(as(user, req{method: http.MethodPost, path: "/api/v1/push/subscriptions", body: body}))
		var sub model.PushSubscriptionInfo
		if err := json.Unmarshal(w.Body.Bytes(), &sub); err != nil || w.Code != http.StatusCreated {
			t.Fatalf("subscribe = %d %s", w.Code, w.Body)
		}
		if w := e.verify(user, sub.ID, e.code(t, d)); w.Code != http.StatusOK {
			t.Fatalf("verify = %d %s", w.Code, w.Body)
		}
		return d
	}
	on := verified("alice", "Phone", true)
	off := verified("alice", "Laptop", false)
	pending := newPushDevice(t, e.f)
	if status, _ := e.subscribe(t, "alice", pending, "Tablet"); status != http.StatusCreated {
		t.Fatalf("subscribe = %d", status)
	}
	bobs := verified("bob", "Phone", true)
	e.svc.Wait()
	before := map[string]int{}
	for name, d := range map[string]pushDevice{"on": on, "off": off, "pending": pending, "bob": bobs} {
		before[name] = len(d.messages(t, e.f))
	}

	ref := plugin.ActionRef{
		Instance: "aa", Spec: plugin.ActionSpec{Name: "ping", Title: "Ping", MinRole: auth.RoleAdmin, Summary: func(any) string { return "Ping aa" }},
		ConfirmFor: func(context.Context, any) plugin.Confirm { return plugin.ConfirmNone },
	}
	p := auth.Principal{Kind: auth.KindToken, User: "alice", Role: auth.RoleAdmin, Audience: model.TokenAudienceMCP, TokenID: "agenttoken", Scopes: []string{"actions:aa"}}
	c := agentCall{P: p, ClientName: "Test Agent", Ref: ref}
	for range 2 { // the second call is the same request: no second push
		a, d := e.s.checkAgentAction(context.Background(), c)
		if d != nil {
			t.Fatalf("check = %+v", d)
		}
		if _, _, d := e.s.requestApproval(c, a); d != nil {
			t.Fatalf("request = %+v", d)
		}
	}
	e.svc.Wait()
	info := e.s.approvals.Pending("alice")[0]
	msgs := on.messages(t, e.f)[before["on"]:]
	if len(msgs) != 1 {
		t.Fatalf("the device taking approvals got %d pushes: %+v", len(msgs), msgs)
	}
	if m := msgs[0]; m.Type != model.PushTypeApproval || m.ID != info.ID || m.URL != testOrigin+"/approve/"+info.ID ||
		!strings.Contains(m.Body, "Ping aa") || !strings.Contains(m.Body, "Test Agent (as it names itself)") {
		t.Errorf("push = %+v", m)
	}
	for name, d := range map[string]pushDevice{"off": off, "pending": pending, "bob": bobs} {
		if got := d.messages(t, e.f)[before[name]:]; len(got) != 0 {
			t.Errorf("%s got %+v", name, got)
		}
	}
}

// agentGrant is the principal of a new mcp OAuth grant of user holding scopes.
func (e *approvalEnv) agentGrant(t *testing.T, user string, scopes ...string) auth.Principal {
	t.Helper()
	id, err := credentials.NewGrantID()
	if err != nil {
		t.Fatal(err)
	}
	_, hash, err := credentials.NewRefreshToken(id)
	if err != nil {
		t.Fatal(err)
	}
	now := e.clk.now()
	if err := e.store.AddGrant(credentials.Grant{
		ID: id, User: user, ClientID: "https://agent.example.com/client.json", ClientName: "Agent", ClientKind: "metadata",
		Audience: model.TokenAudienceMCP, Scopes: scopes, RefreshHash: hash, CreatedAt: now, ExpiresAt: now.Add(credentials.GrantLifetime), RefreshedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return auth.Principal{Kind: auth.KindToken, User: user, Role: auth.RoleAdmin, GrantID: id, ClientID: "https://agent.example.com/client.json",
		Audience: model.TokenAudienceMCP, Scopes: scopes}
}

// A request made through an OAuth grant runs while the grant is active; a revoked grant ends it
// denied, with nothing run.
func TestApprovalOfARevokedGrantIsDenied(t *testing.T) {
	e := newApprovalEnv(t, nil)
	g := e.agentGrant(t, "alice", "actions:aa")
	ok := e.mustAsk(t, g, "restart", okRestart)
	if got := approvalOf(t, e.decide("alice", ok.ID, model.ApprovalDecision{Approve: true})); got.State != model.ApprovalStateApproved || got.CredentialName != "Agent" {
		t.Fatalf("active grant = %+v", got)
	}
	revoked := e.mustAsk(t, g, "restart", restartIn{Namespace: "apps", Name: "api"})
	if _, _, err := e.store.RevokeGrant(g.GrantID); err != nil {
		t.Fatal(err)
	}
	got := approvalOf(t, e.decide("alice", revoked.ID, model.ApprovalDecision{Approve: true}))
	if got.State != model.ApprovalStateDenied || !strings.Contains(got.Reason, "revoked") {
		t.Errorf("revoked grant = %+v", got)
	}
	if n := len(e.acts()); n != 1 {
		t.Errorf("Act ran %d times, want once (the active grant's)", n)
	}
}

// A rule tightened after the request (a namespace newly protected) is enforced at approval: the
// request, made without a typed name, is denied and nothing runs.
func TestApprovalOfARuleTightenedSinceIsDenied(t *testing.T) {
	t.Cleanup(func() { protectedLater.Store(false) })
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "wipe", restartIn{Namespace: "later", Name: "db"})
	if info.Confirm != plugin.ConfirmDialog {
		t.Fatalf("rule = %q", info.Confirm)
	}
	protectedLater.Store(true)
	got := approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}))
	if got.State != model.ApprovalStateDenied || !strings.Contains(got.Reason, "typed") {
		t.Errorf("decision = %+v", got)
	}
	if n := len(e.acts()); n != 0 {
		t.Errorf("Act ran %d times", n)
	}
}

// The run does not end with the deciding request (a phone whose connection drops): Act's context
// is not cancelled with it. It does end at approvalRunTimeout.
func TestApprovalRunOutlivesTheRequestAndEndsAtItsTimeout(t *testing.T) {
	e := newApprovalEnv(t, nil)
	p := e.agent(t, "alice", "actions:aa")
	var sawErr error
	e.actCtx = func(ctx context.Context, _ string, _ any) (any, error) {
		sawErr = ctx.Err()
		return nil, ctx.Err()
	}
	info := e.mustAsk(t, p, "restart", okRestart)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, _ := json.Marshal(model.ApprovalDecision{Approve: true})
	hr := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/approvals/"+info.ID+"/decision", strings.NewReader(string(b)))
	hr.Host, hr.RemoteAddr = testHost, testPeer
	for k, v := range map[string]string{"X-Forwarded-Preferred-Username": "alice", "X-Dash-Csrf": "1", "Origin": testOrigin, "Content-Type": "application/json"} {
		hr.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.Handler().ServeHTTP(w, hr)
	if got := approvalOf(t, w); got.State != model.ApprovalStateApproved || got.Outcome == nil || !got.Outcome.OK || sawErr != nil {
		t.Fatalf("with the request cancelled: %+v (Act saw %v)", got, sawErr)
	}

	prev := approvalRunTimeout
	approvalRunTimeout = 50 * time.Millisecond
	t.Cleanup(func() { approvalRunTimeout = prev })
	e.actCtx = func(ctx context.Context, _ string, _ any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	slow := e.mustAsk(t, p, "restart", restartIn{Namespace: "apps", Name: "slow"})
	start := time.Now()
	got := approvalOf(t, e.decide("alice", slow.ID, model.ApprovalDecision{Approve: true}))
	if got.State != model.ApprovalStateApproved || got.Outcome == nil || got.Outcome.OK || time.Since(start) > 5*time.Second {
		t.Errorf("past the timeout: %+v after %v", got, time.Since(start))
	}
}

// A secret in an approved action's answer is scrubbed from the stored outcome.
func TestApprovalOutcomeIsScrubbed(t *testing.T) {
	leak, _, _, err := credentials.NewPAT()
	if err != nil {
		t.Fatal(err)
	}
	e := newApprovalEnv(t, func(string, any) (any, error) { return map[string]string{"token": leak}, nil })
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}))
	got := approvalOf(t, e.get("alice", info.ID))
	if got.Outcome == nil || !got.Outcome.OK || strings.Contains(got.Outcome.Result, leak) || !strings.Contains(got.Outcome.Result, "token") {
		t.Errorf("outcome = %+v", got.Outcome)
	}
}

// A panic in the plugin's code before the action runs (its Validate) answers 500 and gives the
// request back to pending: nothing ran, and a later approval runs it.
func TestApprovalPanicGivesTheRequestBack(t *testing.T) {
	t.Cleanup(func() { fragilePanics.Store(false) })
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "fragile", fragileIn{Name: "x"})
	fragilePanics.Store(true)
	wantError(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}), http.StatusInternalServerError, model.ErrCodeInternal)
	if got := approvalOf(t, e.get("alice", info.ID)); got.State != model.ApprovalStatePending {
		t.Fatalf("after the panic: %+v", got)
	}
	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times", n)
	}
	fragilePanics.Store(false)
	if got := approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true})); got.State != model.ApprovalStateApproved {
		t.Errorf("the later approval = %+v", got)
	}
}

// core.approvals ends as the store stands, however creations and decisions interleave.
func TestCoreApprovalsEndsCurrent(t *testing.T) {
	e := newApprovalEnv(t, nil)
	p := e.agent(t, "alice", "actions:aa")
	for round := range 20 {
		var wg sync.WaitGroup
		for i := range 8 {
			wg.Go(func() {
				info, _, d := e.ask(t, p, "restart", restartIn{Namespace: "apps", Name: "w-" + strings.Repeat("x", round) + string(rune('a'+i))})
				if d == nil && i%2 == 0 {
					e.decide("alice", info.ID, model.ApprovalDecision{Approve: false})
				}
			})
		}
		wg.Wait()
		want, got := e.approvals.Pending("alice"), e.pendingFor(t, "alice")
		if len(got) != len(want) {
			t.Fatalf("round %d: core.approvals has %d, the store %d", round, len(got), len(want))
		}
		for _, info := range want {
			e.decide("alice", info.ID, model.ApprovalDecision{Approve: false})
		}
	}
}

// panicOnMarshal is an action answer whose encoding panics: dash fails after the action ran.
type panicOnMarshal struct{}

func (panicOnMarshal) MarshalJSON() ([]byte, error) { panic("answer cannot be encoded") }

// A panic after the action ran (keeping its answer) finishes the request as approved with a
// failed outcome: the action ran once, and a second decision is 409, never a second run.
func TestApprovalThatRanIsNeverRunAgain(t *testing.T) {
	e := newApprovalEnv(t, func(string, any) (any, error) { return panicOnMarshal{}, nil })
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	got := approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}))
	if got.State != model.ApprovalStateApproved || got.Outcome == nil || got.Outcome.OK ||
		!strings.Contains(got.Outcome.Error, "dash failed after the action ran") {
		t.Fatalf("decision = %+v %+v", got, got.Outcome)
	}
	wantError(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: true}), http.StatusConflict, model.ErrCodeConflict)
	if n := len(e.acts()); n != 1 {
		t.Errorf("Act ran %d times, want once", n)
	}
}

// Two publishes for one user at once end with the latest state: a create's publish holds the list
// it read (one request) when a decision empties it; the decision's publish must come after, so
// core.approvals ends empty, not with the stale request.
func TestCoreApprovalsPublishOrder(t *testing.T) {
	e := newApprovalEnv(t, nil)
	info := e.mustAsk(t, e.agent(t, "alice", "actions:aa"), "restart", okRestart)
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.approvalsPublishGap = func(string) {
		first := false
		once.Do(func() { first = true })
		if first {
			close(reached)
			<-release
		}
	}
	stale := make(chan struct{})
	go func() { defer close(stale); e.publishApprovals("alice") }() // the create's publish, holding [info]
	<-reached
	decided := make(chan struct{})
	go func() {
		defer close(decided)
		approvalOf(t, e.decide("alice", info.ID, model.ApprovalDecision{Approve: false}))
	}()
	// Unordered, the decision publishes its empty list now, before the stale one; ordered, it
	// waits for the create's publish to finish.
	select {
	case <-decided:
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	<-stale
	<-decided
	if got := e.pendingFor(t, "alice"); len(got) != 0 {
		t.Errorf("core.approvals ends with %d requests, want none", len(got))
	}
}
