package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// noneAuth is the authenticator of auth.mode=none, as cmd/dash builds it.
func noneAuth() auth.Authenticator {
	return auth.Fixed(auth.Principal{Kind: auth.KindNone, User: "anonymous", Role: auth.RoleAdmin})
}

// Every request is the anonymous administrator: no header, a header naming someone else, a peer
// outside any trusted set and a bearer credential all get the same principal.
func TestNoneModeSignsEveryoneInAsAdmin(t *testing.T) {
	s := newServer(withAuth(noneAuth()))
	var seen seenByNext
	h := behindAuthn(s, &seen)

	for _, tc := range []struct {
		name string
		r    req
	}{
		{"no identity header", req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}},
		{"a header naming someone else", req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-User": "mallory"}}},
		{"a peer outside every trusted set", req{path: "/api/v1/me", peer: "192.0.2.1:1234"}},
		{"a bearer credential", req{path: "/api/v1/me", header: map[string]string{"Authorization": "Bearer abc"}}},
		{"an unparsable peer", req{path: "/api/v1/me", peer: "not-an-address"}},
	} {
		seen = seenByNext{}
		w := tc.r.do(h)
		if w.Code != http.StatusNoContent || seen.ran != 1 {
			t.Errorf("%s: status %d, ran %d; want it signed in", tc.name, w.Code, seen.ran)
			continue
		}
		if p := seen.p; p.Kind != auth.KindNone || p.User != "anonymous" || p.Role != auth.RoleAdmin || p.Via != "fixed" || seen.user != "anonymous" {
			t.Errorf("%s: principal = %+v user %q, want the anonymous admin", tc.name, p, seen.user)
		}
	}
}

// The anonymous administrator reaches an admin-only action through the whole chain, and CSRF
// still applies: none mode exempts nothing.
func TestNoneModeStillEnforcesCSRFAndActsAsAdmin(t *testing.T) {
	e := newActionEnv(t, nil)
	e.d.Auth = noneAuth()
	h := d2Chain(e.Server)
	ping := "/api/v1/plugins/aa/actions/ping"

	if w := (req{method: http.MethodPost, path: ping, raw: true}).do(h); w.Code != http.StatusForbidden || apiError(t, w).Code != model.ErrCodeCSRFFailed {
		t.Fatalf("a write without the CSRF headers = %d %s, want 403 csrf_failed", w.Code, w.Body)
	}
	if n := len(e.acts()); n != 0 {
		t.Fatalf("Act ran %d times without CSRF", n)
	}
	w := req{method: http.MethodPost, path: ping, peer: "192.0.2.1:1", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(h)
	if w.Code != http.StatusAccepted {
		t.Fatalf("the anonymous admin's ping = %d %s", w.Code, w.Body)
	}
	calls := e.acts()
	if len(calls) != 1 || calls[0].p.Kind != auth.KindNone || calls[0].p.User != "anonymous" || calls[0].p.Role != auth.RoleAdmin {
		t.Errorf("Act calls = %+v, want one as the anonymous admin", calls)
	}
}

// me answers for the anonymous administrator, and the authenticator carries the warning the SPA
// banner is built from.
func TestNoneModeIsAnInteractiveAdminWithAWarning(t *testing.T) {
	s := newServer(withAuth(noneAuth()))
	if got := roleOnMe(t, d2Chain(s), "anonymous", ""); got != "admin" {
		t.Errorf("me.role = %q, want admin", got)
	}
	w, ok := s.d.Auth.(auth.Warner)
	if !ok || len(w.Warnings()) != 1 || w.Warnings()[0] != auth.WarningAuthNone {
		t.Errorf("warnings = %v (Warner %v), want [auth_none]", w, ok)
	}
}

// me names the mode of the fixed authenticator.
func TestMeNamesTheNoneMode(t *testing.T) {
	var me model.Me
	w := req{path: "/api/v1/me"}.do(d2Chain(newServer(withAuth(noneAuth()))))
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || w.Code != http.StatusOK || me.AuthMode != "none" {
		t.Errorf("me = %d %s (%v), want authMode none", w.Code, w.Body, err)
	}
}

// A refusal that happens before any principal exists (CSRF on a path outside /api, where authn
// does not run) is recorded under the mode of the fixed authenticator, not as proxy (D4).
func TestRefusalWithoutAPrincipalRecordsTheFixedMode(t *testing.T) {
	for _, kind := range []auth.Kind{auth.KindNone, auth.KindDev, auth.KindDemo} {
		t.Run(string(kind), func(t *testing.T) {
			dir := t.TempDir()
			s := newServer(func(d *Deps) {
				d.Audit = openAudit(t, dir, newAuditClock().now)
				d.Auth = auth.Fixed(auth.Principal{Kind: kind, User: string(kind), Role: auth.RoleAdmin})
			})
			if w := (req{method: http.MethodPost, path: "/", raw: true}).do(s.Handler()); w.Code != http.StatusForbidden {
				t.Fatalf("POST / without CSRF = %d %s, want 403", w.Code, w.Body)
			}
			recs := auditRecords(t, dir)
			if len(recs) != 1 || recs[0].Reason != obs.AuthCSRF.String() || recs[0].AuthMode != string(kind) || recs[0].Actor.Type != string(kind) {
				t.Errorf("audit = %+v, want one csrf denial with mode and actor type %q", recs, kind)
			}
		})
	}
}

// The mode is the kind of the principal the authenticator returned, whatever the process
// configuration says: me reports "session" for a session principal even when dev mode is set.
type authenticatorFunc func(*http.Request) (auth.Principal, *auth.Failure)

func (f authenticatorFunc) Authenticate(r *http.Request) (auth.Principal, *auth.Failure) { return f(r) }

func TestMeNamesThePrincipalKind(t *testing.T) {
	a := authenticatorFunc(func(*http.Request) (auth.Principal, *auth.Failure) {
		return auth.Principal{Kind: auth.KindSession, User: "sam", Role: auth.RoleViewer}, nil
	})
	s := newServer(func(d *Deps) {
		d.Auth = a
		d.Cfg.Dev = true
	})
	var me model.Me
	w := req{path: "/api/v1/me"}.do(d2Chain(s))
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || w.Code != http.StatusOK || me.AuthMode != "session" {
		t.Errorf("me = %d %s (%v), want authMode session", w.Code, w.Body, err)
	}
}
