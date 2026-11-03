package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/oidcauth/oidctest"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
)

// oidcResolved is a resolved configuration in auth.mode=oidc whose issuer nobody serves (dash
// discovers it on the first login only), changed by mod.
func oidcResolved(t *testing.T, mod func(*config.AuthOIDCConfig)) *config.Resolved {
	t.Helper()
	return localResolved(t.TempDir(), testKeyring(t), func(a *config.AuthConfig) {
		a.Mode = config.AuthModeOIDC
		a.OIDC.Issuer, a.OIDC.ClientID = "https://127.0.0.1:1/realms/none", "dash"
		a.OIDC.ClientSecret = secret.New("client-secret-value", "")
		a.OIDC.AllowedUsers = []string{"alice", "bob"}
		if mod != nil {
			mod(&a.OIDC)
		}
	})
}

// oidcServer is the API of pa, for requests through the whole chain.
func oidcServer(pa processAuth, res *config.Resolved) http.Handler {
	d := api.Deps{Cfg: res.Config, Version: "test", Hub: hub.New()}
	d.Cfg.Limits = config.Defaults().Limits
	pa.deps(&d)
	return api.New(d).Handler()
}

// meWith asks /api/v1/me with cookie (none when nil).
func meWith(t *testing.T, h http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.Host, r.RemoteAddr = "dash.example.com", "127.0.0.1:41000"
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The oidc mode starts although its provider cannot be reached (discovery waits for the first
// login), and a 401 names the mode and the provider's host for the login page.
func TestOIDCModeStartsWithAnUnreachableIssuer(t *testing.T) {
	res := oidcResolved(t, nil)
	var logs logCapture
	pa, _, err := startLocal(t, res, "", &logs)
	if err != nil || pa.sess == nil || pa.sess.oidc == nil {
		t.Fatalf("start: %+v %v", pa, err)
	}
	if len(logs.with(t, "INFO", "auth: oidc mode")) != 1 {
		t.Error("no startup line")
	}
	w := meWith(t, oidcServer(pa, res), nil)
	var e struct {
		Code    string            `json:"code"`
		Details model.AuthDetails `json:"details"`
	}
	if w.Code != http.StatusUnauthorized || json.Unmarshal(w.Body.Bytes(), &e) != nil ||
		e.Details.Mode != "oidc" || e.Details.OIDC == nil || e.Details.OIDC.Issuer != "127.0.0.1:1" {
		t.Errorf("401 = %d %s, want details {mode oidc, oidc {issuer host}}", w.Code, w.Body)
	}
}

// The state cookie of /auth/oidc/start is sealed with the key of session.PurposeOIDCState (not
// the session key) and lives 10 minutes; both are pinned here as values, not derived from the
// constants the code uses. (From the v0.12.0 security verification.)
func TestOIDCStateCookieHasItsOwnKeyAndTenMinutes(t *testing.T) {
	idp := oidctest.New(t, "dash", "client-secret-value", "https://dash.example.com/auth/callback", nil)
	res := oidcResolved(t, func(o *config.AuthOIDCConfig) { o.Issuer = idp.Issuer() })
	pa, _, err := startLocal(t, res, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().Unix()
	_, state, err := pa.sess.oidc.Start(context.Background(), "/pods")
	if err != nil {
		t.Fatal(err)
	}
	if state.MaxAge != 600 {
		t.Errorf("state cookie Max-Age %d, want 600 (10 minutes)", state.MaxAge)
	}
	open := func(purpose string) (map[string]any, error) {
		key, err := session.DeriveKey(res.Keys, purpose, pa.sess.store.SessionSalt())
		if err != nil {
			t.Fatal(err)
		}
		c, err := session.NewCodec(key)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		err = c.Open(state.Value, &v)
		return v, err
	}
	v, err := open("dash/oidc-state/v1")
	if err != nil {
		t.Fatalf("the state cookie does not open with the dash/oidc-state/v1 key: %v", err)
	}
	// The sealed expiry is 10 minutes after the start too (a few seconds of slack for the test).
	if exp, _ := v["exp"].(float64); int64(exp) < before+600 || int64(exp) > time.Now().Unix()+600 {
		t.Errorf("sealed expiry %v, want 600 s after %d", v["exp"], before)
	}
	for _, purpose := range []string{"dash/session/v1", "dash/session/v1/oidc"} {
		if _, err := open(purpose); err == nil {
			t.Errorf("the state cookie opens with the %s key", purpose)
		}
	}
}

// allowedUsers ["*"] opens dash to every account the provider signs in: a WARN at every start,
// and oidc_open in Me.warnings for administrators only.
func TestOIDCWildcardWarnsAndShowsOidcOpen(t *testing.T) {
	res := oidcResolved(t, func(o *config.AuthOIDCConfig) {
		o.AllowedUsers = []string{"*"}
		o.AdminUsers = []string{"alice"}
	})
	var logs logCapture
	pa, _, err := startLocal(t, res, "", &logs)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.with(t, "WARN", oidcOpenWarning)) != 1 {
		t.Error("the open allow-list was not warned about at the start")
	}
	h := oidcServer(pa, res)
	for user, want := range map[string]bool{"alice": true, "bob": false} {
		c, err := pa.sess.sessions.Issue(session.Identity{User: user}, session.Keep{})
		if err != nil {
			t.Fatal(err)
		}
		var me model.Me
		w := meWith(t, h, c)
		if json.Unmarshal(w.Body.Bytes(), &me) != nil || me.AuthMode != "oidc" || slices.Contains(me.Warnings, auth.WarningOIDCOpen) != want {
			t.Errorf("%s: me = %d %s, oidc_open shown = %v", user, w.Code, w.Body, want)
		}
	}
	// A closed allow-list says nothing.
	closed, _, err := startLocal(t, oidcResolved(t, nil), "", &logs)
	if err != nil {
		t.Fatal(err)
	}
	if ws := closed.sess.authn.Warnings(); slices.Contains(ws, auth.WarningOIDCOpen) {
		t.Errorf("warnings = %v without the wildcard", ws)
	}
}

// adminUsers, adminGroups and adminRoles are live: a reload maps the same cookie differently on
// the next request, without a restart.
func TestOIDCAdminMappingReloadsOnTheNextRequest(t *testing.T) {
	res := oidcResolved(t, func(o *config.AuthOIDCConfig) {
		o.AllowedGroups = []string{"ops"}
		o.RoleClaim = "roles"
		o.AdminRoles = []string{"dash-admin"}
	})
	pa, _, err := startLocal(t, res, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := pa.sess.sessions.Issue(session.Identity{User: "bob", Groups: []string{"ops"}, RoleClaim: []string{"reader"}},
		session.Keep{Groups: []string{"ops"}, RoleClaim: []string{"reader", "dash-admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRole(pa.authn, c); got != auth.RoleViewer {
		t.Fatalf("before: %q, want viewer", got)
	}
	for name, mod := range map[string]func(*config.AuthOIDCConfig){
		"adminUsers":  func(o *config.AuthOIDCConfig) { o.AdminUsers = []string{"bob"} },
		"adminGroups": func(o *config.AuthOIDCConfig) { o.AdminGroups = []string{"ops"} },
		"adminRoles":  func(o *config.AuthOIDCConfig) { o.AdminRoles = []string{"reader"} },
	} {
		o := res.Tree.Auth.OIDC
		mod(&o)
		applyOIDCReload(pa.sess.roles, o)
		if got := sessionRole(pa.authn, c); got != auth.RoleAdmin {
			t.Errorf("after reloading %s: %q, want admin", name, got)
		}
		applyOIDCReload(pa.sess.roles, res.Tree.Auth.OIDC)
		if got := sessionRole(pa.authn, c); got != auth.RoleViewer {
			t.Errorf("after reverting %s: %q, want viewer", name, got)
		}
	}
}
