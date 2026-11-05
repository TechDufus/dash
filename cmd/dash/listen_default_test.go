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

// A sidecar install that kept the loopback trustedProxies but not the loopback listener warns at
// start; a listener on loopback, or a trusted proxy outside loopback (in another pod), does not.
func TestProxyModeWarnsWhenOnlyLoopbackProxiesAreTrustedOffLoopback(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  []string
		warn bool
	}{
		{"default listen, default proxies", nil, true},
		{"default listen, 127.0.0.0/8 and ::1", []string{`DASH_AUTH_PROXY_TRUSTED_PROXIES=["127.0.0.0/8","::1"]`}, true},
		{"loopback listen", []string{"DASH_SERVER_LISTEN=127.0.0.1:8080"}, false},
		{"ipv6 loopback listen", []string{"DASH_SERVER_LISTEN=[::1]:8080"}, false},
		{"a proxy in another pod", []string{`DASH_AUTH_PROXY_TRUSTED_PROXIES=["127.0.0.1/32","10.42.0.0/16"]`}, false},
		{"a proxy address", []string{`DASH_AUTH_PROXY_TRUSTED_PROXIES=["192.0.2.7"]`}, false},
		// A prefix wider than the loopback range starts at a loopback address but is not loopback.
		{"127.0.0.0/1", []string{`DASH_AUTH_PROXY_TRUSTED_PROXIES=["127.0.0.0/1"]`}, false},
		{"::1/64", []string{`DASH_AUTH_PROXY_TRUSTED_PROXIES=["::1/64"]`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := config.Resolve(config.Sources{
				Environ:  append([]string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com", "DASH_AUTH_MODE=proxy"}, tc.env...),
				Registry: secret.NewRegistry(),
			})
			if err != nil {
				t.Fatal(err)
			}
			var logs logCapture
			if _, _, err := buildAuthenticator(res, logs.logger()); err != nil {
				t.Fatal(err)
			}
			got := logs.with(t, "WARN", proxyListenWarning)
			if (len(got) == 1) != tc.warn || len(got) > 1 {
				t.Fatalf("WARN %v, want %v:\n%s", got, tc.warn, &logs.buf)
			}
			if tc.warn && got[0]["listen"] != res.Config.Listen {
				t.Errorf("WARN names listen %v, want %s", got[0]["listen"], res.Config.Listen)
			}
		})
	}
}
