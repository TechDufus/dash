package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// seenByNext is what the handler behind authn saw.
type seenByNext struct {
	ran  int
	p    auth.Principal
	user string
}

// behindAuthn is s.authn wrapped around a handler that records the principal it is given. The
// chain under test is authn alone: the host check, CSRF and the guard are other middleware.
func behindAuthn(s *Server, seen *seenByNext) http.Handler {
	return s.authn(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.ran++
		seen.p, seen.user = principalFrom(r.Context()), userFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))
}

// auditedServer is a server with a real audit log and metrics, so a refusal's record and count
// can be read back. dir holds the audit file.
func auditedServer(t *testing.T, mod func(*Deps)) (s *Server, dir string) {
	t.Helper()
	dir = t.TempDir()
	s = newServer(func(d *Deps) {
		d.Metrics = obs.New("test", RoutePatterns(), nil)
		d.Audit = openAudit(t, dir, newAuditClock().now)
		if mod != nil {
			mod(d)
		}
	})
	return s, dir
}

func TestAuthnStoresThePrincipalForTheHandlers(t *testing.T) {
	s := newServer(nil) // alice is the one admin
	var seen seenByNext
	h := behindAuthn(s, &seen)

	w := req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": "", "X-Forwarded-User": "alice"}}.do(h)
	if w.Code != http.StatusNoContent || seen.ran != 1 {
		t.Fatalf("status %d, handler ran %d times", w.Code, seen.ran)
	}
	if seen.p.Kind != auth.KindProxy || seen.p.User != "alice" || seen.p.Role != auth.RoleAdmin || seen.p.Via != "X-Forwarded-User" {
		t.Errorf("principal = %+v", seen.p)
	}
	if seen.user != "alice" {
		t.Errorf("userFrom = %q, want alice", seen.user)
	}
}

func TestAuthnReportsTheFirstSightingToTheProbe(t *testing.T) {
	buf := &syncBuf{}
	s := newServer(nil)
	s.probe.log = slog.New(slog.NewTextHandler(buf, nil)) // before the first request
	var seen seenByNext
	h := behindAuthn(s, &seen)

	for range 2 {
		if w := (req{path: "/api/v1/me"}).do(h); w.Code != http.StatusNoContent {
			t.Fatalf("status %d", w.Code)
		}
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("probe wrote %d lines, want 1:\n%s", len(lines), buf)
	}
	for _, want := range []string{"principal seen", "user=alice", "kind=proxy", "role=admin", "peer=127.0.0.1", "via=X-Forwarded-Preferred-Username"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("probe line lacks %q: %s", want, lines[0])
		}
	}
}

func TestAuthnRefusesWithTheFailuresStatusAndCode(t *testing.T) {
	for _, tc := range []struct {
		name       string
		req        req
		wantCode   string
		wantReason string // the audit record's reason
	}{
		{"no identity", req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}, model.ErrCodeUnauthenticated, obs.AuthUnauthenticated.String()},
		{"bearer", req{path: "/api/v1/me", header: map[string]string{"Authorization": "Bearer abc"}}, model.ErrCodeInvalidToken, obs.AuthUnauthenticated.String()},
		{"untrusted peer", req{path: "/api/v1/me", peer: "192.0.2.1:1234"}, model.ErrCodeUnauthenticated, obs.AuthUntrustedPeer.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, dir := auditedServer(t, nil)
			var seen seenByNext
			w := tc.req.do(behindAuthn(s, &seen))
			if w.Code != http.StatusUnauthorized || seen.ran != 0 {
				t.Fatalf("status %d, handler ran %d times; want 401 and not at all", w.Code, seen.ran)
			}
			if got := apiError(t, w).Code; got != tc.wantCode {
				t.Errorf("code = %q, want %q", got, tc.wantCode)
			}
			if h := w.Header().Get("WWW-Authenticate"); h != "" {
				t.Errorf("WWW-Authenticate = %q, want none until bearer tokens exist", h)
			}
			recs := auditRecords(t, dir)
			if len(recs) != 1 || recs[0].Kind != audit.KindDenial || recs[0].Reason != tc.wantReason {
				t.Errorf("audit = %+v, want one denial with reason %q", recs, tc.wantReason)
			}
		})
	}
}

// Two header lines are one denial counted under their own label; the other refusals that are not
// the untrusted peer count as unauthenticated.
func TestAuthnTwoUserHeaderLinesIsOneAmbiguousDenial(t *testing.T) {
	s, dir := auditedServer(t, nil)
	var seen seenByNext
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.Host, r.RemoteAddr = testHost, testPeer
	r.Header.Add("X-Forwarded-User", "mallory")
	r.Header.Add("X-Forwarded-User", "alice")
	w := httptest.NewRecorder()
	behindAuthn(s, &seen).ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized || seen.ran != 0 || apiError(t, w).Code != model.ErrCodeUnauthenticated {
		t.Fatalf("status %d (handler ran %d), body %s", w.Code, seen.ran, w.Body)
	}
	recs := auditRecords(t, dir)
	if len(recs) != 1 || recs[0].Reason != obs.AuthAmbiguousIdentity.String() {
		t.Errorf("audit = %+v, want one denial with reason %q", recs, obs.AuthAmbiguousIdentity)
	}
}

func TestAuthnCountsTheRefusalUnderUnauthenticated(t *testing.T) {
	s, _ := auditedServer(t, nil)
	var seen seenByNext
	h := behindAuthn(s, &seen)
	req{path: "/api/v1/me", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(h)
	req{path: "/api/v1/me", header: map[string]string{"Authorization": "Bearer abc"}}.do(h)

	ss := scrapeMetrics(t, s)
	v, ok := seriesValue(t, ss, "dash_auth_failures_total", map[string]string{"reason": obs.AuthUnauthenticated.String()})
	if !ok || v != 2 {
		t.Errorf("unauthenticated counted %v (present %v), want 2: no identity and a refused bearer", v, ok)
	}
}

// authn decides for the API only: the SPA and its assets need no principal.
func TestAuthnSkipsPathsOutsideTheAPI(t *testing.T) {
	s := newServer(nil)
	var seen seenByNext
	h := behindAuthn(s, &seen)
	for _, path := range []string{"/", "/index.html", "/assets/app-1.js", "/apiary", "/cluster/pods"} {
		seen = seenByNext{}
		w := req{path: path, peer: "192.0.2.1:1", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(h)
		if w.Code != http.StatusNoContent || seen.ran != 1 || seen.p.User != "" {
			t.Errorf("%s: status %d, ran %d, principal %+v; want it passed through with none", path, w.Code, seen.ran, seen.p)
		}
	}
	for _, path := range []string{"/api", "/api/", "/api/v1/me", "/api/nope"} {
		seen = seenByNext{}
		w := req{path: path, peer: "192.0.2.1:1", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(h)
		if w.Code != http.StatusUnauthorized || seen.ran != 0 {
			t.Errorf("%s: status %d, ran %d; want 401", path, w.Code, seen.ran)
		}
	}
}

type challengeAuth struct{}

func (challengeAuth) Authenticate(*http.Request) (auth.Principal, *auth.Failure) {
	return auth.Principal{}, &auth.Failure{Status: http.StatusUnauthorized, Code: auth.CodeUnauthenticated, Reason: auth.ReasonNoIdentity, WWWAuth: `Bearer realm="dash"`}
}

func TestAuthnSendsTheFailuresChallenge(t *testing.T) {
	s := newServer(withAuth(challengeAuth{}))
	var seen seenByNext
	w := req{path: "/api/v1/me"}.do(behindAuthn(s, &seen))
	if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != `Bearer realm="dash"` {
		t.Errorf("status %d, challenge %q", w.Code, w.Header().Get("WWW-Authenticate"))
	}
}

func TestAuthDenialReasonMapsTheFailureReasons(t *testing.T) {
	for reason, want := range map[string]obs.AuthReason{
		auth.ReasonUntrustedPeer:     obs.AuthUntrustedPeer,
		auth.ReasonAmbiguousIdentity: obs.AuthAmbiguousIdentity,
		auth.ReasonNoIdentity:        obs.AuthUnauthenticated,
		auth.ReasonBearerRefused:     obs.AuthUnauthenticated,
		"something_later":            obs.AuthUnauthenticated,
	} {
		if got := authDenialReason(&auth.Failure{Reason: reason}); got != want {
			t.Errorf("%s -> %v, want %v", reason, got, want)
		}
	}
}

// The authenticator the other tests default to refuses a bearer even from the trusted peer.
func TestAuthnBearerIgnoresIdentityHeaders(t *testing.T) {
	s := newServer(withAuth(authtest.Proxy("alice")))
	var seen seenByNext
	w := req{path: "/api/v1/me", header: map[string]string{"Authorization": "Bearer abc", "X-Forwarded-User": "alice"}}.do(behindAuthn(s, &seen))
	if w.Code != http.StatusUnauthorized || seen.ran != 0 || apiError(t, w).Code != model.ErrCodeInvalidToken {
		t.Errorf("status %d, ran %d, body %s; want 401 invalid_token", w.Code, seen.ran, w.Body)
	}
}
