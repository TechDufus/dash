package api

import (
	"net/http"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/obs"
)

// A request that carries X-Forwarded-User from a peer outside the trusted set is 401, one
// untrusted_peer denial, and never reaches the handler.
func TestUntrustedPeerGets401(t *testing.T) {
	s, dir := auditedServer(t, nil)
	var seen seenByNext
	w := req{path: "/api/v1/me", peer: "192.0.2.1:1234", header: map[string]string{"X-Forwarded-User": "alice"}}.do(behindAuthn(s, &seen))

	if w.Code != http.StatusUnauthorized || seen.ran != 0 {
		t.Fatalf("status %d, handler ran %d times; want 401 and not at all", w.Code, seen.ran)
	}
	if e := apiError(t, w); e.Code != model.ErrCodeUnauthenticated || e.Error != "unauthenticated" {
		t.Errorf("error = %+v", e)
	}
	recs := auditRecords(t, dir)
	if len(recs) != 1 || recs[0].Reason != obs.AuthUntrustedPeer.String() || recs[0].Peer != "192.0.2.1" {
		t.Errorf("audit = %+v, want one untrusted_peer denial for peer 192.0.2.1", recs)
	}
}

func TestTrustedPeerIsAccepted(t *testing.T) {
	s := newServer(nil)
	for _, peer := range []string{"127.0.0.1:41000", "[::1]:41000", "[::ffff:127.0.0.1]:41000"} {
		var seen seenByNext
		w := req{path: "/api/v1/me", peer: peer}.do(behindAuthn(s, &seen))
		if w.Code != http.StatusNoContent || seen.ran != 1 || seen.p.User != "alice" {
			t.Errorf("peer %s: status %d, ran %d, user %q; want accepted as alice", peer, w.Code, seen.ran, seen.p.User)
		}
	}

	// The trusted set is the configured one, not loopback: a CIDR from the setting is believed
	// and the loopback, outside it, is not.
	trust, err := auth.ParseTrust([]string{"10.20.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	a := auth.NewProxy(trust, auth.ProxyIdentity{UserHeaders: []string{"X-Forwarded-User"}}, auth.RoleMap{})
	s = newServer(withAuth(a))
	for peer, code := range map[string]int{"10.20.255.1:1": http.StatusNoContent, "10.21.0.1:1": http.StatusUnauthorized, "127.0.0.1:1": http.StatusUnauthorized} {
		var seen seenByNext
		w := req{path: "/api/v1/me", peer: peer, header: map[string]string{"X-Forwarded-User": "alice"}}.do(behindAuthn(s, &seen))
		if w.Code != code {
			t.Errorf("peer %s: status %d, want %d", peer, w.Code, code)
		}
	}
}

// X-Forwarded-For, Forwarded and X-Real-Ip are client-controlled: naming the proxy's address in
// them from an untrusted peer does not make that peer trusted.
func TestSpoofedForwardedForDoesNotTrustAPeer(t *testing.T) {
	s := newServer(nil)
	var seen seenByNext
	w := req{path: "/api/v1/me", peer: "192.0.2.1:1234", header: map[string]string{
		"X-Forwarded-For": "127.0.0.1", "Forwarded": "for=127.0.0.1", "X-Real-Ip": "127.0.0.1",
	}}.do(behindAuthn(s, &seen))
	if w.Code != http.StatusUnauthorized || seen.ran != 0 {
		t.Errorf("status %d, ran %d; want 401 from the untrusted peer", w.Code, seen.ran)
	}
}
