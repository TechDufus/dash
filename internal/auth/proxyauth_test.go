package auth

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// stepClock is an injected clock: it moves only when the test says so.
type stepClock struct{ t time.Time }

func (c *stepClock) now() time.Time          { return c.t }
func (c *stepClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newStepClock() *stepClock { return &stepClock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)} }

// logBuf captures what the authenticator logs. Authenticate is called from the test goroutine
// only, so no lock is needed.
type logBuf struct{ bytes.Buffer }

func (b *logBuf) logger() *slog.Logger {
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// lines are the log lines written so far.
func (b *logBuf) lines() []string {
	s := strings.TrimSpace(b.String())
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

var (
	testIdentity = ProxyIdentity{UserHeaders: []string{"X-Forwarded-Preferred-Username", "X-Forwarded-User"}}
	loopbackOnly = mustTrust("127.0.0.1/32", "::1/128")
)

func mustTrust(entries ...string) Trust {
	t, err := ParseTrust(entries)
	if err != nil {
		panic(err)
	}
	return t
}

// request is a request from peer with the given header pairs (name, value, name, value, ...);
// the same name twice makes two header lines.
func request(peer string, kv ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.RemoteAddr = peer
	for i := 0; i < len(kv); i += 2 {
		r.Header.Add(kv[i], kv[i+1])
	}
	return r
}

func wantFailure(t *testing.T, f *Failure, code, reason string) {
	t.Helper()
	if f == nil {
		t.Fatalf("authenticated, want 401 %s/%s", code, reason)
	}
	if f.Status != http.StatusUnauthorized || f.Code != code || f.Reason != reason {
		t.Errorf("failure = %d %s/%s, want 401 %s/%s", f.Status, f.Code, f.Reason, code, reason)
	}
}

func TestTrustedPeerAuthenticatesAsAProxyPrincipal(t *testing.T) {
	p := NewProxy(loopbackOnly, testIdentity, RoleMap{AdminUsers: []string{"alice"}})
	for _, peer := range []string{"127.0.0.1:41000", "[::1]:41000", "[::ffff:127.0.0.1]:41000"} {
		got, f := p.Authenticate(request(peer, "X-Forwarded-User", "alice"))
		if f != nil {
			t.Fatalf("peer %s: refused: %+v", peer, f)
		}
		if got.Kind != KindProxy || got.User != "alice" || got.Role != RoleAdmin || !got.Interactive() {
			t.Errorf("peer %s: principal = %+v", peer, got)
		}
	}
}

func TestUntrustedPeerIsRefusedWithItsOwnReason(t *testing.T) {
	p := NewProxy(loopbackOnly, testIdentity, RoleMap{AdminUsers: []string{"*"}})
	// Identity headers from outside the trusted set are the untrusted_peer refusal.
	_, f := p.Authenticate(request("192.0.2.1:1234", "X-Forwarded-User", "alice"))
	wantFailure(t, f, CodeUnauthenticated, ReasonUntrustedPeer)
	// The same peer with no identity header is only unauthenticated.
	_, f = p.Authenticate(request("192.0.2.1:1234"))
	wantFailure(t, f, CodeUnauthenticated, ReasonNoIdentity)
	// An unparsable peer is untrusted too.
	_, f = p.Authenticate(request("not-an-address", "X-Forwarded-User", "alice"))
	wantFailure(t, f, CodeUnauthenticated, ReasonUntrustedPeer)
	// A trusted peer with no header has no identity.
	_, f = p.Authenticate(request("127.0.0.1:1"))
	wantFailure(t, f, CodeUnauthenticated, ReasonNoIdentity)
}

func TestSpoofedForwardedForDoesNotTrustAPeer(t *testing.T) {
	p := NewProxy(loopbackOnly, testIdentity, RoleMap{AdminUsers: []string{"*"}})
	r := request("192.0.2.1:1234", "X-Forwarded-User", "alice", "X-Forwarded-For", "127.0.0.1", "Forwarded", "for=127.0.0.1", "X-Real-Ip", "127.0.0.1")
	_, f := p.Authenticate(r)
	wantFailure(t, f, CodeUnauthenticated, ReasonUntrustedPeer)
}

func TestUntrustedPeerWarnIsRateLimited(t *testing.T) {
	var buf logBuf
	clock := newStepClock()
	p := NewProxy(loopbackOnly, testIdentity, RoleMap{}, WithLogger(buf.logger()), WithClock(clock.now))
	refuse := func(user string) {
		t.Helper()
		_, f := p.Authenticate(request("192.0.2.1:1234", "X-Forwarded-User", user))
		wantFailure(t, f, CodeUnauthenticated, ReasonUntrustedPeer)
	}

	refuse("mallory-1")
	if n := len(buf.lines()); n != 1 {
		t.Fatalf("first refusal logged %d lines, want 1:\n%s", n, &buf)
	}
	line := buf.lines()[0]
	if !strings.Contains(line, "level=WARN") || !strings.Contains(line, "192.0.2.1") || !strings.Contains(line, "auth.proxy.trustedProxies") {
		t.Errorf("warning = %q, want a WARN naming the peer and the setting", line)
	}
	if strings.Contains(line, "mallory") {
		t.Errorf("warning carries a header value: %q", line)
	}

	refuse("mallory-2")
	clock.advance(warnEvery - time.Nanosecond)
	refuse("mallory-3")
	if n := len(buf.lines()); n != 1 {
		t.Fatalf("%d lines inside the minute, want still 1:\n%s", n, &buf)
	}
	clock.advance(time.Nanosecond)
	refuse("mallory-4")
	if n := len(buf.lines()); n != 2 {
		t.Fatalf("%d lines after the minute, want 2:\n%s", n, &buf)
	}

	// A peer without identity headers is not a topology problem and never warns.
	clock.advance(time.Hour)
	if _, f := p.Authenticate(request("192.0.2.1:1234")); f == nil {
		t.Fatal("authenticated without a header")
	}
	if n := len(buf.lines()); n != 2 {
		t.Errorf("a request without identity headers logged: %d lines\n%s", n, &buf)
	}
}

func TestBearerIgnoresIdentityHeaders(t *testing.T) {
	var buf logBuf
	p := NewProxy(loopbackOnly, testIdentity, RoleMap{AdminUsers: []string{"*"}}, WithLogger(buf.logger()))
	for _, tc := range []struct{ name, peer, auth string }{
		{"trusted peer", "127.0.0.1:1", "Bearer abc"},
		{"scheme is case-insensitive", "127.0.0.1:1", "bEaReR abc"},
		{"empty token", "127.0.0.1:1", "Bearer"},
		{"untrusted peer", "192.0.2.1:1", "Bearer abc"},
	} {
		got, f := p.Authenticate(request(tc.peer, "Authorization", tc.auth, "X-Forwarded-User", "alice"))
		if f == nil {
			t.Errorf("%s: authenticated as %+v", tc.name, got)
			continue
		}
		if f.Status != http.StatusUnauthorized || f.Code != CodeInvalidToken || f.Reason != ReasonBearerRefused {
			t.Errorf("%s: failure = %+v, want 401 invalid_token/bearer_refused", tc.name, f)
		}
		if got.User != "" {
			t.Errorf("%s: the refusal still names %q", tc.name, got.User)
		}
	}
	if len(buf.lines()) != 0 {
		t.Errorf("a refused bearer logged: %s", &buf)
	}

	// Any other scheme is ignored: a proxy that forwards Basic credentials changes nothing.
	got, f := p.Authenticate(request("127.0.0.1:1", "Authorization", "Basic YWxpY2U6cHc=", "X-Forwarded-User", "alice"))
	if f != nil || got.User != "alice" {
		t.Errorf("Basic: %+v %+v, want alice", got, f)
	}
}

func TestTwoUserHeaderLinesAreRefused(t *testing.T) {
	p := NewProxy(loopbackOnly, testIdentity, RoleMap{AdminUsers: []string{"*"}})
	for _, tc := range []struct {
		name string
		kv   []string
	}{
		{"first header twice", []string{"X-Forwarded-Preferred-Username", "mallory", "X-Forwarded-Preferred-Username", "alice"}},
		{"the used header twice, the first one empty", []string{"X-Forwarded-Preferred-Username", " ", "X-Forwarded-User", "mallory", "X-Forwarded-User", "alice"}},
		{"two identical lines", []string{"X-Forwarded-User", "alice", "X-Forwarded-User", "alice"}},
	} {
		_, f := p.Authenticate(request("127.0.0.1:1", tc.kv...))
		if f == nil {
			t.Errorf("%s: authenticated", tc.name)
			continue
		}
		wantFailure(t, f, CodeUnauthenticated, ReasonAmbiguousIdentity)
	}

	// The groups header is part of the identity: two lines of it are ambiguous as well.
	g := NewProxy(loopbackOnly, ProxyIdentity{UserHeaders: testIdentity.UserHeaders, GroupsHeader: "X-Forwarded-Groups"}, RoleMap{})
	_, f := g.Authenticate(request("127.0.0.1:1", "X-Forwarded-User", "alice", "X-Forwarded-Groups", "admins", "X-Forwarded-Groups", "ops"))
	wantFailure(t, f, CodeUnauthenticated, ReasonAmbiguousIdentity)
}

func TestDisagreeingUserHeadersWarn(t *testing.T) {
	var buf logBuf
	clock := newStepClock()
	p := NewProxy(loopbackOnly, testIdentity, RoleMap{}, WithLogger(buf.logger()), WithClock(clock.now))
	both := func(a, b string) Principal {
		t.Helper()
		got, f := p.Authenticate(request("127.0.0.1:1", "X-Forwarded-Preferred-Username", a, "X-Forwarded-User", b))
		if f != nil {
			t.Fatalf("refused: %+v", f)
		}
		return got
	}

	// Agreeing headers, or one header, are silent.
	both("alice", "alice")
	if _, f := p.Authenticate(request("127.0.0.1:1", "X-Forwarded-User", "alice")); f != nil {
		t.Fatal(f)
	}
	if len(buf.lines()) != 0 {
		t.Fatalf("agreeing headers warned:\n%s", &buf)
	}

	// Disagreeing headers: the first configured one wins, one WARN names the headers, not the values.
	if got := both("alice", "bob-secret"); got.User != "alice" {
		t.Errorf("user = %q, want the first header's alice", got.User)
	}
	if len(buf.lines()) != 1 {
		t.Fatalf("want one warning:\n%s", &buf)
	}
	line := buf.lines()[0]
	for _, want := range []string{"level=WARN", "X-Forwarded-Preferred-Username", "X-Forwarded-User"} {
		if !strings.Contains(line, want) {
			t.Errorf("warning lacks %q: %s", want, line)
		}
	}
	if strings.Contains(line, "bob-secret") || strings.Contains(line, "alice") {
		t.Errorf("warning carries a header value: %s", line)
	}

	// At most once a minute.
	both("alice", "bob-secret")
	clock.advance(warnEvery - time.Nanosecond)
	both("alice", "bob-secret")
	if n := len(buf.lines()); n != 1 {
		t.Fatalf("%d warnings inside the minute, want 1", n)
	}
	clock.advance(time.Nanosecond)
	both("alice", "bob-secret")
	if n := len(buf.lines()); n != 2 {
		t.Fatalf("%d warnings after the minute, want 2", n)
	}

	// The preferred header empty and the other set is a fallback, not a disagreement.
	clock.advance(time.Hour)
	if got := both("", "bob"); got.User != "bob" {
		t.Errorf("user = %q, want the fallback bob", got.User)
	}
	if n := len(buf.lines()); n != 2 {
		t.Errorf("a fallback warned: %d lines", n)
	}
}

func TestViaNamesTheHeader(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   ProxyIdentity
		kv   []string
		via  string
	}{
		{"preferred username", testIdentity, []string{"X-Forwarded-Preferred-Username", "alice", "X-Forwarded-User", "alice"}, "X-Forwarded-Preferred-Username"},
		{"fallback to the subject", testIdentity, []string{"X-Forwarded-User", "alice"}, "X-Forwarded-User"},
		{"configured name is canonicalised", ProxyIdentity{UserHeaders: []string{"x-auth-request-user"}}, []string{"X-Auth-Request-User", "alice"}, "X-Auth-Request-User"},
	} {
		p := NewProxy(loopbackOnly, tc.id, RoleMap{})
		got, f := p.Authenticate(request("127.0.0.1:1", tc.kv...))
		if f != nil {
			t.Errorf("%s: refused: %+v", tc.name, f)
			continue
		}
		if got.Via != tc.via {
			t.Errorf("%s: Via = %q, want %q", tc.name, got.Via, tc.via)
		}
	}
}

func TestUserHeadersOrderAndFallback(t *testing.T) {
	id := ProxyIdentity{UserHeaders: []string{"X-Second", "X-First"}}
	p := NewProxy(loopbackOnly, id, RoleMap{})
	for _, tc := range []struct {
		name string
		kv   []string
		user string // "" = refused
	}{
		{"the configured order decides, not the header order", []string{"X-First", "first", "X-Second", "second"}, "second"},
		{"an absent first header falls through", []string{"X-First", "first"}, "first"},
		{"a blank first header falls through", []string{"X-Second", "  ", "X-First", "first"}, "first"},
		{"the value is trimmed", []string{"X-Second", "  second \t"}, "second"},
		{"an unusable first value does not fall through", []string{"X-Second", "bad\x01name", "X-First", "first"}, ""},
		{"an over-long first value does not fall through", []string{"X-Second", strings.Repeat("a", 129), "X-First", "first"}, ""},
		{"a header that is not configured is not read", []string{"X-Forwarded-User", "alice"}, ""},
	} {
		got, f := p.Authenticate(request("127.0.0.1:1", tc.kv...))
		if tc.user == "" {
			if f == nil {
				t.Errorf("%s: authenticated as %q", tc.name, got.User)
			} else {
				wantFailure(t, f, CodeUnauthenticated, ReasonNoIdentity)
			}
			continue
		}
		if f != nil || got.User != tc.user {
			t.Errorf("%s: %q %+v, want %q", tc.name, got.User, f, tc.user)
		}
	}
}

func TestGroupsHeaderParsing(t *testing.T) {
	with := NewProxy(loopbackOnly, ProxyIdentity{UserHeaders: testIdentity.UserHeaders, GroupsHeader: "X-Forwarded-Groups"}, RoleMap{})
	got, f := with.Authenticate(request("127.0.0.1:1", "X-Forwarded-User", "alice", "X-Forwarded-Groups", " ops, dev ,ops,,admins"))
	if f != nil {
		t.Fatal(f)
	}
	if want := []string{"ops", "dev", "admins"}; strings.Join(got.Groups, "|") != strings.Join(want, "|") {
		t.Errorf("groups = %q, want %q", got.Groups, want)
	}

	// Without the header the user has no groups.
	got, f = with.Authenticate(request("127.0.0.1:1", "X-Forwarded-User", "alice"))
	if f != nil || len(got.Groups) != 0 {
		t.Errorf("no header: %+v %+v", got, f)
	}

	// The groups header is read only when it is configured: a client-supplied one grants nothing.
	without := NewProxy(loopbackOnly, testIdentity, RoleMap{AdminGroups: []string{"admins"}})
	got, f = without.Authenticate(request("127.0.0.1:1", "X-Forwarded-User", "alice", "X-Forwarded-Groups", "admins"))
	if f != nil || len(got.Groups) != 0 {
		t.Errorf("groups header not configured: %+v %+v, want no groups", got, f)
	}
}

func TestFixedIgnoresHeadersPeersAndBearer(t *testing.T) {
	a := Fixed(Principal{Kind: KindDev, User: "dev", Role: RoleAdmin})
	for _, r := range []*http.Request{
		request("192.0.2.1:1"),
		request("127.0.0.1:1", "X-Forwarded-User", "mallory"),
		request("not-an-address", "Authorization", "Bearer abc", "X-Forwarded-User", "mallory"),
	} {
		got, f := a.Authenticate(r)
		if f != nil || got.Kind != KindDev || got.User != "dev" || got.Role != RoleAdmin || got.Via != "fixed" {
			t.Errorf("%s: %+v %+v, want the fixed dev admin via fixed", r.RemoteAddr, got, f)
		}
	}
}
