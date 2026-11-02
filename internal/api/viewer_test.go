package api

import (
	"net/http"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// Every action of every running plugin is 403 for a viewer, the Act is never called, and the
// refusal is one forbidden denial (the guard's; the per-action check in runAction is unreachable).
func TestViewerGets403OnEveryAction(t *testing.T) {
	e := newActionEnv(t, nil)
	e.Server.d.Auth = authtest.Proxy("alice") // vera is a viewer
	h := e.Handler()
	var actions []string
	for _, info := range e.sup.Infos() {
		for _, a := range info.Actions {
			actions = append(actions, "/api/v1/plugins/"+info.ID+"/actions/"+a.Name)
		}
	}
	if len(actions) == 0 {
		t.Fatal("no action to try")
	}
	for _, path := range actions {
		w := req{method: http.MethodPost, path: path, body: map[string]any{}, header: map[string]string{"X-Forwarded-Preferred-Username": "vera"}}.do(h)
		if w.Code != http.StatusForbidden || apiError(t, w).Code != model.ErrCodeForbidden {
			t.Errorf("%s as a viewer = %d %s, want 403 forbidden", path, w.Code, w.Body)
		}
	}
	if n := len(e.acts()); n != 0 {
		t.Errorf("Act ran %d times for a viewer", n)
	}
	// One denial per user and reason a minute: the record names vera and the guard's reason.
	var denials []audit.Record
	for _, r := range auditRecords(t, e.dir) {
		if r.Kind == audit.KindDenial {
			denials = append(denials, r)
		}
	}
	if len(denials) != 1 || denials[0].Reason != obs.AuthForbidden.String() || denials[0].Actor.User != "vera" {
		t.Errorf("denials = %+v, want one forbidden denial naming vera", denials)
	}
}

// /me carries the authenticator's standing warnings, and an empty list (never null) otherwise.
func TestMeCarriesTheAuthenticatorsWarnings(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    auth.Authenticator
		want []string
	}{
		{"an administrator list", authtest.Proxy("alice"), []string{}},
		{"no administrator at all", authtest.Proxy(), []string{}},
		{"the wildcard", authtest.ProxyWith(auth.RoleMap{AdminUsers: []string{"*"}}), []string{"admin_wildcard"}},
		{"a fixed principal", auth.Fixed(auth.Principal{Kind: auth.KindDev, User: "dev", Role: auth.RoleAdmin}), []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := req{path: "/api/v1/me"}.do(newServer(withAuth(tc.a)).Handler())
			var me model.Me
			decodeJSON(t, w, &me)
			if me.Warnings == nil || !slices.Equal(me.Warnings, tc.want) {
				t.Errorf("warnings = %#v, want %#v", me.Warnings, tc.want)
			}
		})
	}
}
