package api

import (
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// capabilities.approvals says whether the caller may see and decide approval requests: MCP is on,
// some plugin has mounted actions, and the caller is a named person or a token with the approvals
// scope; never the anonymous viewer, and never without MCP (auth.mode none has no tokens).
func TestMeCapabilitiesApprovals(t *testing.T) {
	person := auth.Principal{Kind: auth.KindProxy, User: "alice", Role: auth.RoleAdmin}
	token := func(scopes ...string) auth.Principal {
		return auth.Principal{Kind: auth.KindToken, User: "alice", Role: auth.RoleAdmin, Audience: model.TokenAudienceAPI, Scopes: scopes, GrantID: "g1"}
	}
	for _, tc := range []struct {
		name    string
		p       auth.Principal
		mcp     bool
		tokens  bool
		actions bool
		want    bool
	}{
		{"a person", person, true, true, true, true},
		{"a viewer person", auth.Principal{Kind: auth.KindProxy, User: "bob", Role: auth.RoleViewer}, true, true, true, true},
		{"MCP off", person, false, true, true, false},
		{"no tokens (auth.mode none)", person, true, false, true, false},
		{"no mounted action", person, true, true, false, false},
		{"a token with approvals", token("read", model.OAuthScopeApprovals, model.OAuthScopePush), true, true, true, true},
		{"a token without approvals", token("read", "actions:aa"), true, true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newActionEnvWith(t, nil, []plugin.InstanceConfig{{Name: "aa", ActionsEnabled: tc.actions}})
			if tc.tokens {
				e.d.Tokens = stubResolver{}
			}
			e.d.MCPEnabled = func() bool { return tc.mcp }
			e.d.Auth = auth.Fixed(tc.p)
			if got := meOf(t, req{path: "/api/v1/me"}.do(e.Handler())).Capabilities.Approvals; got != tc.want {
				t.Errorf("capabilities.approvals = %v, want %v", got, tc.want)
			}
		})
	}
}
