package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// asPrincipal is r as the auth middleware would leave it for p.
func asPrincipal(r *http.Request, p auth.Principal) *http.Request {
	ctx := context.WithValue(context.WithValue(r.Context(), userKey{}, p.User), principalKey{}, p)
	return r.WithContext(ctx)
}

// guardTable is a synthetic route table with one row per access column the guard decides on.
func guardTable(ran *[]string) []Route {
	h := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			*ran = append(*ran, name)
			w.WriteHeader(http.StatusNoContent)
		}
	}
	return []Route{
		{Method: "GET", Path: "/api/v1/viewer-row", Role: auth.RoleViewer, Scope: "read", Handler: h("viewer")},
		{Method: "GET", Path: "/api/v1/admin-row", Role: auth.RoleAdmin, Scope: "read", Handler: h("admin")},
		{Method: "GET", Path: "/api/v1/logs-row", Role: auth.RoleViewer, Scope: "logs", Handler: h("logs")},
		{Method: "GET", Path: "/api/v1/unscoped-row", Role: auth.RoleViewer, Handler: h("unscoped")},
		{Method: "GET", Path: "/api/v1/person-row", Role: auth.RoleViewer, Interactive: true, Handler: h("person")},
		{Path: "/public", Infra: true, Handler: h("public")},
	}
}

func TestGuardRefusesBelowTheRouteRole(t *testing.T) {
	proxyAdmin := auth.Principal{Kind: auth.KindProxy, User: "ada", Role: auth.RoleAdmin}
	proxyViewer := auth.Principal{Kind: auth.KindProxy, User: "vera", Role: auth.RoleViewer}
	token := auth.Principal{Kind: auth.KindToken, User: "token:ci", Role: auth.RoleAdmin, Scopes: []string{"read"}}
	unknown := auth.Principal{Kind: auth.KindProxy, User: "who", Role: "root"}

	for _, tc := range []struct {
		name string
		path string
		p    *auth.Principal // nil: the request never passed auth
		want int
		ran  string
	}{
		{"admin on an admin row", "/api/v1/admin-row", &proxyAdmin, 204, "admin"},
		{"viewer on an admin row", "/api/v1/admin-row", &proxyViewer, 403, ""},
		{"viewer on a viewer row", "/api/v1/viewer-row", &proxyViewer, 204, "viewer"},
		{"admin on a viewer row", "/api/v1/viewer-row", &proxyAdmin, 204, "viewer"},
		{"no principal on a viewer row", "/api/v1/viewer-row", nil, 403, ""},
		{"an unknown role on a viewer row", "/api/v1/viewer-row", &unknown, 403, ""},
		{"a token on a row that is not interactive", "/api/v1/admin-row", &token, 204, "admin"},
		{"a token on an interactive row", "/api/v1/person-row", &token, 403, ""},
		{"a token without the row's scope", "/api/v1/logs-row", &token, 403, ""},
		{"a token on a row without a scope", "/api/v1/unscoped-row", &token, 403, ""},
		{"a person on a logs row", "/api/v1/logs-row", &proxyViewer, 204, "logs"},
		{"a person on an interactive row", "/api/v1/person-row", &proxyViewer, 204, "person"},
		{"a row without a role is public", "/public", nil, 204, "public"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ran []string
			s := newServer(nil)
			h := s.muxOf(guardTable(&ran))
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.p != nil {
				r = asPrincipal(r, *tc.p)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status = %d %s, want %d", w.Code, w.Body, tc.want)
			}
			if tc.want == http.StatusForbidden {
				code := model.ErrCodeForbidden
				if tc.p != nil && tc.p.Kind == auth.KindToken && tc.path != "/api/v1/person-row" {
					code = model.ErrCodeInsufficientScope
				}
				wantError(t, w, http.StatusForbidden, code)
			}
			if got := append([]string(nil), ran...); (tc.ran == "") != (len(got) == 0) || (tc.ran != "" && got[0] != tc.ran) {
				t.Errorf("handlers that ran = %v, want %q", got, tc.ran)
			}
		})
	}
}

// A refusal by the guard is one denial: one audit record and one count, not two.
func TestGuardCountsAuthForbidden(t *testing.T) {
	dir := t.TempDir()
	s := newServer(func(d *Deps) {
		d.Metrics = obs.New("test", RoutePatterns(), nil)
		d.Audit = openAudit(t, dir, newAuditClock().now)
	})
	var ran []string
	h := s.muxOf(guardTable(&ran))
	vera := auth.Principal{Kind: auth.KindProxy, User: "vera", Role: auth.RoleViewer}
	r := asPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/admin-row", nil), vera)
	r.RemoteAddr = testPeer
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d", w.Code)
	}

	recs := auditRecords(t, dir)
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want one denial", recs)
	}
	if rec := recs[0]; rec.Kind != audit.KindDenial || rec.Reason != obs.AuthForbidden.String() || rec.Actor.User != "vera" {
		t.Errorf("denial = %+v", rec)
	}
	ss := scrapeMetrics(t, s)
	if v, ok := seriesValue(t, ss, "dash_auth_failures_total", map[string]string{"reason": obs.AuthForbidden.String()}); !ok || v != 1 {
		t.Errorf("forbidden counted %v (present %v), want 1", v, ok)
	}
}

// The principal the guard judges is the one the authenticator mapped: behind authn, the user the
// role map names reaches an admin row and everyone else is a viewer and is refused there, once.
func TestGuardRefusesTheViewerTheAuthenticatorMapped(t *testing.T) {
	dir := t.TempDir()
	s := newServer(func(d *Deps) {
		d.Metrics = obs.New("test", RoutePatterns(), nil)
		d.Audit = openAudit(t, dir, newAuditClock().now)
		d.Auth = authtest.Proxy("ada")
	})
	var ran []string
	h := s.authn(s.muxOf(guardTable(&ran)))

	for _, tc := range []struct {
		user, path string
		want       int
	}{
		{"ada", "/api/v1/admin-row", http.StatusNoContent},
		{"ada", "/api/v1/viewer-row", http.StatusNoContent},
		{"vera", "/api/v1/viewer-row", http.StatusNoContent},
		{"vera", "/api/v1/admin-row", http.StatusForbidden},
	} {
		w := req{path: tc.path, header: map[string]string{"X-Forwarded-Preferred-Username": tc.user}}.do(h)
		if w.Code != tc.want {
			t.Errorf("%s on %s = %d %s, want %d", tc.user, tc.path, w.Code, w.Body, tc.want)
		}
	}
	if want := []string{"admin", "viewer", "viewer"}; !slices.Equal(ran, want) {
		t.Errorf("handlers that ran = %v, want %v", ran, want)
	}
	recs := auditRecords(t, dir)
	if len(recs) != 1 || recs[0].Reason != obs.AuthForbidden.String() || recs[0].Actor.User != "vera" {
		t.Errorf("audit = %+v, want one forbidden denial naming vera", recs)
	}
}
