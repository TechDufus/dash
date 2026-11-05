package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
)

// meAs calls the me handler with a principal of the given role: no principal of the chain is a
// viewer until P4.2 maps roles (auth gives everybody admin, middleware.go), so the handler gets a
// request whose context carries one, as auth would have stored it.
func meAs(t *testing.T, s *Server, role auth.Role) model.Me {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	p := auth.Principal{Kind: "proxy", User: "vera", Role: role}
	r = r.WithContext(context.WithValue(context.WithValue(r.Context(), userKey{}, p.User), principalKey{}, p))
	w := httptest.NewRecorder()
	s.me(w, r)
	var me model.Me
	decodeJSON(t, w, &me)
	return me
}

// minRoleEnv runs "adm", whose only action needs an admin, and "vwr", whose action a viewer may run.
func minRoleEnv(t *testing.T, mod func(*Deps)) *pluginEnv {
	t.Helper()
	adm := plugintest.New("adm")
	adm.M.Actions = []plugin.ActionSpec{{Name: "wipe", Title: "Wipe"}} // MinRole defaults to admin
	vwr := plugintest.New("vwr")
	vwr.M.Actions = []plugin.ActionSpec{{Name: "look", Title: "Look", MinRole: auth.RoleViewer}}
	e := newPluginEnv(t, []plugin.InstanceConfig{{Name: "adm", ActionsEnabled: true}, {Name: "vwr", ActionsEnabled: true}}, adm, vwr)
	e.eventually(t, "both plugins to run", func() bool { return e.sup.Running("adm") && e.sup.Running("vwr") })
	if mod != nil {
		mod(&e.d)
	}
	return e
}

func TestMeFiltersScopesAndCapabilitiesByActionMinRole(t *testing.T) {
	e := minRoleEnv(t, nil)

	t.Run("a viewer may run a viewer's action but mints no actions scope", func(t *testing.T) {
		me := meAs(t, e.Server, auth.RoleViewer)
		// Scopes are what the person may put on a token (Settings -> Tokens): actions:* is an
		// administrator's.
		if !slices.Equal(me.Scopes, []string{"read", "logs"}) {
			t.Errorf("scopes = %v, want read and logs only", me.Scopes)
		}
		if !me.Capabilities.Actions {
			t.Error("capabilities.actions is false although the viewer has an action")
		}
		if me.Role != "viewer" {
			t.Errorf("role = %q", me.Role)
		}
	})

	t.Run("an admin gets both", func(t *testing.T) {
		for name, me := range map[string]model.Me{
			"through the chain": func() model.Me {
				var me model.Me
				decodeJSON(t, req{path: "/api/v1/me"}.do(e.Handler()), &me)
				return me
			}(),
			"handler with an admin principal": meAs(t, e.Server, auth.RoleAdmin),
		} {
			if !slices.Contains(me.Scopes, "actions:adm") || !slices.Contains(me.Scopes, "actions:vwr") || !me.Capabilities.Actions {
				t.Errorf("%s: scopes %v, actions %v; want both plugins", name, me.Scopes, me.Capabilities.Actions)
			}
			if !slices.Equal(me.Scopes[:2], []string{"read", "logs"}) {
				t.Errorf("%s: scopes = %v, want read and logs first", name, me.Scopes)
			}
		}
	})

	t.Run("a viewer with only admin actions has no capability", func(t *testing.T) {
		adm := plugintest.New("adm")
		adm.M.Actions = []plugin.ActionSpec{{Name: "wipe", Title: "Wipe"}}
		only := newPluginEnv(t, []plugin.InstanceConfig{{Name: "adm", ActionsEnabled: true}}, adm)
		only.eventually(t, "the plugin to run", func() bool { return only.sup.Running("adm") })
		me := meAs(t, only.Server, auth.RoleViewer)
		if me.Capabilities.Actions || !slices.Equal(me.Scopes, []string{"read", "logs"}) {
			t.Errorf("me = %+v, want no action scope and no capability", me)
		}
	})

}

func TestMeIconBaseURLComesFromTheConfiguration(t *testing.T) {
	const base = "https://cdn.example.com/icons"
	for _, tc := range []struct{ name, cfg, want string }{
		{"configured", base, base},
		{"empty means no third-party icon request", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServer(func(d *Deps) { d.Cfg.IconBaseURL = tc.cfg })
			var me model.Me
			decodeJSON(t, req{path: "/api/v1/me"}.do(s.Handler()), &me)
			if me.UI.IconBaseURL != tc.want {
				t.Errorf("ui.iconBaseURL = %q, want %q", me.UI.IconBaseURL, tc.want)
			}
		})
	}
}

// capabilities.oauth says whether dash serves its OAuth authorization server, which decides
// whether the SPA offers Settings -> Connected apps.
func TestMeCapabilitiesOAuth(t *testing.T) {
	for _, on := range []bool{true, false} {
		var me model.Me
		decodeJSON(t, req{path: "/api/v1/me"}.do(newServer(func(d *Deps) {
			if on {
				d.OAuth = testOAuth(t, d)
			}
		}).Handler()), &me)
		if me.Capabilities.OAuth != on {
			t.Errorf("OAuth server %v: capabilities.oauth = %v", on, me.Capabilities.OAuth)
		}
	}
}

func TestMeAnswerCarriesTheUIKeyAndTheFilteredActionsOfPluginsList(t *testing.T) {
	e := minRoleEnv(t, func(d *Deps) { d.Cfg.IconBaseURL = "https://cdn.example.com/icons" })
	w := req{path: "/api/v1/me"}.do(e.Handler())
	if body := w.Body.String(); !strings.Contains(body, `"ui":{"iconBaseUrl":"https://cdn.example.com/icons"}`) {
		t.Errorf("body = %s", body)
	}
}
