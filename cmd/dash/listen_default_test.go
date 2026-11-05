package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/secret"
)

// resolveOnDefaultListen resolves environ (plus the public URL) and requires the API listener to
// be the v0.14.0 default, 0.0.0.0:8080: nothing in environ names server.listen.
func resolveOnDefaultListen(t *testing.T, environ ...string) *config.Resolved {
	t.Helper()
	res, err := config.Resolve(config.Sources{
		Environ:  append([]string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com"}, environ...),
		Registry: secret.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.Listen != "0.0.0.0:8080" {
		t.Fatalf("Listen = %q, want the default 0.0.0.0:8080", res.Config.Listen)
	}
	return res
}

// On the default listener, which any address can reach, the default local mode answers an API
// request without a session with 401, whether or not it carries a proxy's identity header.
func TestLocalModeOnTheDefaultListenRefusesUnauthenticatedRequests(t *testing.T) {
	res := resolveOnDefaultListen(t, "DASH_DATA_DIR="+t.TempDir())
	if res.Tree.Auth.Mode != config.AuthModeLocal {
		t.Fatalf("auth.mode = %q, want the default local", res.Tree.Auth.Mode)
	}
	pa, _, err := startLocal(t, res, hashOf(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	h := oidcServer(pa, res)
	for _, header := range []string{"", "X-Forwarded-User"} {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		r.Host, r.RemoteAddr = "dash.example.com", "192.0.2.1:41000"
		if header != "" {
			r.Header.Set(header, "admin")
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET /api/v1/me from a remote peer (header %q) = %d, want 401", header, w.Code)
		}
	}
}

// The proxy mode on the default listener still believes identity headers only from the loopback
// peers: a remote client that reaches 0.0.0.0:8080 directly cannot name a user.
func TestProxyModeOnTheDefaultListenRefusesRemotePeers(t *testing.T) {
	res := resolveOnDefaultListen(t, "DASH_AUTH_MODE=proxy", `DASH_AUTH_PROXY_ADMIN_USERS=["alice"]`)
	a, _, err := buildAuthenticator(res, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if p, f := a.Authenticate(authRequest("127.0.0.1:41000", "X-Forwarded-User", "alice")); f != nil || p.Role != auth.RoleAdmin {
		t.Errorf("loopback sidecar: %+v %+v, want alice as admin", p, f)
	}
	for _, peer := range []string{"192.0.2.1:1234", "10.0.0.5:1234", "[2001:db8::1]:1234"} {
		if _, f := a.Authenticate(authRequest(peer, "X-Forwarded-User", "alice")); f == nil || f.Status != http.StatusUnauthorized || f.Reason != auth.ReasonUntrustedPeer {
			t.Errorf("peer %s: failure %+v, want 401 %s", peer, f, auth.ReasonUntrustedPeer)
		}
	}
}
