package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// Auth runs before CSRF: an unauthenticated write is 401, and the audit record of a CSRF denial
// names the user the authenticator resolved (the authenticator here reads no header, so only
// authn can have named it).
func TestAuthRunsBeforeCSRF(t *testing.T) {
	const post = "/api/v1/plugins/aa/actions/ping"
	anon := newServer(nil).Handler()
	if w := (req{method: http.MethodPost, path: post, raw: true, header: map[string]string{"X-Forwarded-Preferred-Username": ""}}).do(anon); w.Code != http.StatusUnauthorized || apiError(t, w).Code != model.ErrCodeUnauthenticated {
		t.Errorf("unauthenticated POST without CSRF = %d %s, want 401 unauthenticated", w.Code, w.Body)
	}

	dir := t.TempDir()
	s := newServer(func(d *Deps) {
		d.Audit = openAudit(t, dir, newAuditClock().now)
		d.Auth = auth.Fixed(auth.Principal{Kind: auth.KindProxy, User: "resolved-by-authn", Role: auth.RoleAdmin})
	})
	w := req{method: http.MethodPost, path: post, raw: true, header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(s.Handler())
	if w.Code != http.StatusForbidden || apiError(t, w).Code != model.ErrCodeCSRFFailed {
		t.Fatalf("authenticated POST without CSRF = %d %s, want 403 csrf_failed", w.Code, w.Body)
	}
	recs := auditRecords(t, dir)
	if len(recs) != 1 || recs[0].Reason != obs.AuthCSRF.String() || recs[0].Actor.User != "resolved-by-authn" {
		t.Errorf("audit = %+v, want one csrf denial naming resolved-by-authn", recs)
	}
}

// The SPA and its assets need no principal, from any peer.
func TestStaticSPANeedsNoPrincipal(t *testing.T) {
	s := newServer(nil)
	s.static = fstest.MapFS{
		"index.html":      {Data: []byte("<html>spa</html>")},
		"assets/app-1.js": {Data: []byte("console.log(1)")},
	}
	h := s.Handler()
	for _, path := range []string{"/", "/cluster/pods/x", "/assets/app-1.js"} {
		w := req{path: path, peer: "192.0.2.1:1234", header: map[string]string{"X-Forwarded-Preferred-Username": ""}}.do(h)
		if w.Code != http.StatusOK {
			t.Errorf("%s from an untrusted peer without identity = %d, want 200", path, w.Code)
		}
	}
	if w := (req{path: "/api/v1/me", peer: "192.0.2.1:1234"}).do(h); w.Code != http.StatusUnauthorized {
		t.Errorf("/api/v1/me from the untrusted peer = %d, want 401", w.Code)
	}
}

// An inbound X-Request-Id is believed only from a trusted peer.
func TestRequestIDBelievedOnlyFromTrustedPeers(t *testing.T) {
	h := newServer(nil).Handler()
	for peer, believed := range map[string]bool{"127.0.0.1:41000": true, "[::1]:41000": true, "192.0.2.1:1234": false, "not-an-address": false} {
		w := req{path: "/api/v1/me", peer: peer, header: map[string]string{"X-Request-Id": "inbound-id-1"}}.do(h)
		if got := w.Header().Get(requestIDHeader) == "inbound-id-1"; got != believed {
			t.Errorf("peer %s: inbound id kept = %v, want %v", peer, got, believed)
		}
	}

	// The process modes have no trusted set of their own: the loopback, where a sidecar proxy
	// connects from, is believed and nobody else.
	for name, a := range map[string]auth.Authenticator{
		"none": noneAuth(),
		"dev":  auth.Fixed(auth.Principal{Kind: auth.KindDev, User: "dev", Role: auth.RoleAdmin}),
		"demo": auth.Fixed(auth.Principal{Kind: auth.KindDemo, User: "demo", Role: auth.RoleAdmin}),
	} {
		fixed := newServer(withAuth(a)).Handler()
		for peer, believed := range map[string]bool{"127.0.0.1:41000": true, "[::1]:41000": true, "192.0.2.1:1234": false, "not-an-address": false} {
			w := req{path: "/api/v1/me", peer: peer, header: map[string]string{"X-Request-Id": "inbound-id-1"}}.do(fixed)
			if got := w.Header().Get(requestIDHeader) == "inbound-id-1"; got != believed {
				t.Errorf("%s mode, peer %s: inbound id kept = %v, want %v", name, peer, got, believed)
			}
		}
	}

	// The set is the authenticator's, not the loopback: a proxy on another address is believed
	// once it is in the trusted set.
	trust, err := auth.ParseTrust([]string{"10.20.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	other := newServer(func(d *Deps) { d.Auth = auth.NewProxy(trust, authtest.Identity(), auth.RoleMap{}) }).Handler()
	for peer, believed := range map[string]bool{"10.20.1.5:1": true, "127.0.0.1:1": false} {
		w := req{path: "/api/v1/me", peer: peer, header: map[string]string{"X-Request-Id": "inbound-id-1"}}.do(other)
		if got := w.Header().Get(requestIDHeader) == "inbound-id-1"; got != believed {
			t.Errorf("trusted set 10.20.0.0/16, peer %s: inbound id kept = %v, want %v", peer, got, believed)
		}
	}
}

// Denials are limited to one record per (user, or the peer when there is none) and reason per
// minute: behind a sidecar the peer is constant, so two users' refusals are two records.
func TestDenialDedupKeysOnTheUserWhenThereIsOne(t *testing.T) {
	dir := t.TempDir()
	clk := newAuditClock()
	s := newServer(func(d *Deps) {
		d.Audit = openAudit(t, dir, clk.now)
		d.Auth = authtest.Proxy() // everyone a viewer
	})
	var ran []string
	h := s.authn(s.muxOf(guardTable(&ran)))
	get := func(user, peer string) {
		hdr := map[string]string{"X-Forwarded-Preferred-Username": user}
		req{path: "/api/v1/admin-row", peer: peer, header: hdr}.do(h)
	}
	count := func() (n int) {
		for _, r := range auditRecords(t, dir) {
			if r.Kind == audit.KindDenial {
				n++
			}
		}
		return n
	}
	get("vera", testPeer)
	get("vick", testPeer) // another user behind the same peer: a record of its own
	get("vera", testPeer) // the same user again within the minute: none
	if n := count(); n != 2 {
		t.Fatalf("forbidden denials = %d, want 2 (vera, vick)", n)
	}
	// No user: the peer is the key.
	get("", "192.0.2.1:1")
	get("", "192.0.2.1:2")
	get("", "192.0.2.9:1")
	if n := count(); n != 4 {
		t.Errorf("denials after the user-less requests = %d, want 4 (one per peer)", n)
	}
	clk.advance(61e9)
	get("vera", testPeer)
	if n := count(); n != 5 {
		t.Errorf("denials a minute later = %d, want 5", n)
	}
}

// Both new refusals are counted under their own label.
func TestUntrustedPeerAndAmbiguousIdentityAreCountedUnderTheirOwnLabels(t *testing.T) {
	s := newServer(func(d *Deps) { d.Metrics = obs.New("test", RoutePatterns(), nil) })
	h := s.Handler()
	req{path: "/api/v1/me", peer: "192.0.2.1:1"}.do(h)            // untrusted peer
	two := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil) // two lines of one user header
	two.Host, two.RemoteAddr = testHost, testPeer
	two.Header.Add("X-Forwarded-User", "mallory")
	two.Header.Add("X-Forwarded-User", "alice")
	h.ServeHTTP(httptest.NewRecorder(), two)

	ss := scrapeMetrics(t, s)
	for _, reason := range []obs.AuthReason{obs.AuthUntrustedPeer, obs.AuthAmbiguousIdentity} {
		if v, ok := seriesValue(t, ss, "dash_auth_failures_total", map[string]string{"reason": reason.String()}); !ok || v != 1 {
			t.Errorf("%s counted %v (present %v), want 1", reason, v, ok)
		}
	}
}
