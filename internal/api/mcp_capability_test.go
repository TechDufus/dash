package api

import (
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
)

type stubResolver struct{}

func (stubResolver) Resolve(string, string) (auth.Principal, error) { return auth.Principal{}, nil }

// capabilities.mcp follows mcp.enabled as it is at each request, and is never true without
// bearer tokens (auth.mode none).
func TestMeCapabilityMCPFollowsTheLiveSetting(t *testing.T) {
	enabled := false
	for _, tc := range []struct {
		name    string
		tokens  bool
		setting bool
		want    bool
	}{
		{"off", true, false, false},
		{"on", true, true, true},
		{"on without tokens (none mode)", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(func(d *Deps) {
				if tc.tokens {
					d.Tokens = stubResolver{}
				}
				d.MCPEnabled = func() bool { return enabled }
			})
			enabled = tc.setting
			var me model.Me
			decodeJSON(t, req{path: "/api/v1/me"}.do(s.Handler()), &me)
			if me.Capabilities.MCP != tc.want {
				t.Errorf("capabilities.mcp = %t, want %t", me.Capabilities.MCP, tc.want)
			}
			// The setting is read per request: switching it changes the next answer.
			enabled = !tc.setting
			decodeJSON(t, req{path: "/api/v1/me"}.do(s.Handler()), &me)
			if me.Capabilities.MCP != (tc.tokens && !tc.setting) {
				t.Errorf("after the switch capabilities.mcp = %t", me.Capabilities.MCP)
			}
		})
	}
	if s := newServer(nil); s.mcpEnabled() {
		t.Error("MCP is on without MCPEnabled")
	}
}
