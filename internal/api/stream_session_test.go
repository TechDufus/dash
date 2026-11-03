package api

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/session"
)

// ended waits for the stream to close (the server ended it), failing after a few seconds. It
// returns how many heartbeats arrived first.
func ended(t *testing.T, lines <-chan string, what string) int {
	t.Helper()
	pings := 0
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				return pings
			}
			if l == ": ping" {
				pings++
			}
		case <-timeout:
			t.Fatalf("%s: the stream is still open after %d heartbeats", what, pings)
			return pings
		}
	}
}

// sessionCookie issues a session for user and renders it as a Cookie header.
func cookieFor(t *testing.T, h *sessionHarness, user string) map[string]string {
	t.Helper()
	c, err := h.Sessions.Issue(session.Identity{User: user}, session.Keep{})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"Cookie": c.Name + "=" + c.Value, "X-Forwarded-Preferred-Username": ""}
}

// An open event stream ends at the first heartbeat after its session is revoked: "Log out" (the
// user's epoch) ends that user's streams only, "Log out everywhere" (the global epoch) every
// session stream. The browser then reconnects, gets 401 and signs in again.
func TestSessionStreamEndsWhenTheSessionIsRevoked(t *testing.T) {
	opt, h := withSessions(t, auth.ModeLocal, auth.RoleMap{AdminUsers: []string{"alice"}}, false, nil)
	s := newServer(opt)
	s.heartbeat = 20 * time.Millisecond
	handler := s.Handler()
	_, alice := sseClientWith(t, handler, "/api/v1/stream", cookieFor(t, h, "alice"))
	bobCookie := cookieFor(t, h, "bob")
	_, bob := sseClientWith(t, handler, "/api/v1/stream", bobCookie)
	expect(t, alice, ": ping")
	expect(t, bob, ": ping")

	if err := h.Store.BumpEpoch("alice"); err != nil {
		t.Fatal(err)
	}
	ended(t, alice, "alice after her logout")
	expect(t, bob, ": ping") // another user's session is untouched
	expect(t, bob, ": ping")

	if err := h.Store.BumpGlobal(); err != nil {
		t.Fatal(err)
	}
	ended(t, bob, "bob after a logout everywhere")
	// Reconnecting with the revoked cookie is refused; a fresh login streams again.
	if r, _ := sseClientWith(t, handler, "/api/v1/stream", bobCookie); r.StatusCode != http.StatusUnauthorized {
		t.Errorf("reconnect with the revoked cookie: %d, want 401", r.StatusCode)
	}
	if r, _ := sseClientWith(t, handler, "/api/v1/stream", cookieFor(t, h, "bob")); r.StatusCode != http.StatusOK {
		t.Errorf("a fresh session after the bump: %d, want 200", r.StatusCode)
	}
}

// A stream of the proxy mode has no session to lose: the check never ends it.
func TestProxyStreamIsNotEndedByTheSessionCheck(t *testing.T) {
	s := newServer(nil)
	s.heartbeat = 20 * time.Millisecond
	_, lines := sseClient(t, s.Handler(), "/api/v1/stream")
	for range 5 {
		expect(t, lines, ": ping")
	}
}

// The log tail (a plugin stream) ends the same way, and gives its slot back. With the anonymous
// viewer on, a revoked session's tail does not carry on as the anonymous viewer, not even for an
// account that is itself named "anonymous": the re-check wants a session principal, not only the
// same user name.
func TestSessionLogTailEndsWhenTheSessionIsRevoked(t *testing.T) {
	for _, tc := range []struct {
		anonymous bool
		user      string
	}{{false, "alice"}, {true, "alice"}, {true, "anonymous"}} {
		t.Run(fmt.Sprintf("anonymous viewer %v, user %s", tc.anonymous, tc.user), func(t *testing.T) {
			h := newHarness(t)
			opt, sh := withSessions(t, auth.ModeLocal, auth.RoleMap{AdminUsers: []string{tc.user}}, tc.anonymous, nil)
			var d Deps
			opt(&d)
			h.Server.d.Auth = d.Auth
			h.heartbeat = 20 * time.Millisecond
			st := h.openStream(t, tailPath, "", cookieFor(t, sh, tc.user))
			if st.status() != http.StatusOK {
				t.Fatalf("tail open = %d", st.status())
			}
			st.expect(": ping")
			if err := sh.Store.BumpEpoch(tc.user); err != nil {
				t.Fatal(err)
			}
			ended(t, st.lines, "the tail after the logout")
			h.eventually(t, "the tail's slot to be released", func() bool { return h.openTails() == 0 })
		})
	}
}
