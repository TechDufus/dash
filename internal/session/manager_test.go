package session_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/session/sessiontest"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// clock is an injected clock: it moves only when the test says so.
type clock struct{ t time.Time }

func newClock() *clock                     { return &clock{t: time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)} }
func (c *clock) now() time.Time            { return c.t }
func (c *clock) set(at time.Time)          { c.t = at }
func (c *clock) advance(d time.Duration)   { c.t = c.t.Add(d) }
func (c *clock) since(t0 time.Time) string { return c.t.Sub(t0).String() }

// memStore is an in-memory credential store: the real Epochs implementation.
func memStore(t *testing.T) *credentials.Store {
	t.Helper()
	s, err := credentials.Open("")
	must(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// withCookies is a GET request carrying cs.
func withCookies(cs ...*http.Cookie) *http.Request {
	return requestWith(http.MethodGet, cs...)
}

func requestWith(method string, cs ...*http.Cookie) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/me", nil)
	r.RemoteAddr = "127.0.0.1:41000"
	for _, c := range cs {
		r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	return r
}

// payloadOf opens a cookie the sessiontest codec sealed.
func payloadOf(t *testing.T, c *http.Cookie) session.Payload {
	t.Helper()
	var p session.Payload
	must(t, sessiontest.Codec().Open(c.Value, &p))
	return p
}

func issue(t *testing.T, m *session.Manager, user string) *http.Cookie {
	t.Helper()
	c, err := m.Issue(session.Identity{User: user}, session.Keep{})
	must(t, err)
	return c
}

func wantErr(t *testing.T, m *session.Manager, c *http.Cookie, want error) {
	t.Helper()
	if _, _, err := m.Read(withCookies(c)); !errors.Is(err, want) {
		t.Fatalf("Read = %v, want %v", err, want)
	}
}

func TestCookieIsInvalidAfterAnEpochBump(t *testing.T) {
	store := memStore(t)
	m := sessiontest.Manager(store, newClock().now)
	c := issue(t, m, "alice")
	if p, _, err := m.Read(withCookies(c)); err != nil || p.User != "alice" {
		t.Fatalf("fresh cookie: %+v, %v", p, err)
	}
	must(t, store.BumpEpoch("alice"))
	wantErr(t, m, c, session.ErrRevoked)

	// A login after the bump carries the new epoch; the global epoch kills it as well.
	c = issue(t, m, "alice")
	if p := payloadOf(t, c); p.UserEpoch != 1 || p.GlobalEpoch != 0 {
		t.Fatalf("new cookie epochs ep=%d ge=%d, want 1 0", p.UserEpoch, p.GlobalEpoch)
	}
	wantErr(t, m, c, nil)
	must(t, store.BumpGlobal())
	wantErr(t, m, c, session.ErrRevoked)
	if p := payloadOf(t, issue(t, m, "alice")); p.GlobalEpoch != 1 {
		t.Fatalf("ge=%d after the global bump, want 1", p.GlobalEpoch)
	}
}

func TestPerUserEpochInvalidatesOnlyThatUser(t *testing.T) {
	store := memStore(t)
	m := sessiontest.Manager(store, newClock().now)
	alice, bob := issue(t, m, "alice"), issue(t, m, "bob")
	must(t, store.BumpEpoch("bob")) // the second epoch row: the store holds one account
	wantErr(t, m, alice, nil)
	wantErr(t, m, bob, session.ErrRevoked)
}

func TestSessionCookieNameFollowsTheScheme(t *testing.T) {
	for _, tc := range []struct {
		secure bool
		name   string
	}{{true, "__Host-dash_session"}, {false, "dash_session"}} {
		m := session.NewManager(session.Options{Codec: sessiontest.Codec(), Epochs: memStore(t), Secure: tc.secure, Now: newClock().now})
		if m.CookieName() != tc.name || session.CookieName(tc.secure) != tc.name {
			t.Errorf("secure=%v: name %q, want %q", tc.secure, m.CookieName(), tc.name)
		}
		c := issue(t, m, "alice")
		set := c.String()
		for _, want := range []string{tc.name + "=v1.", "Path=/", "HttpOnly", "SameSite=Lax", "Max-Age=43200"} {
			if !strings.Contains(set, want) {
				t.Errorf("secure=%v: Set-Cookie %q lacks %q", tc.secure, set, want)
			}
		}
		if got := strings.Contains(set, "; Secure"); got != tc.secure {
			t.Errorf("secure=%v: Secure attribute present=%v", tc.secure, got)
		}
		if strings.Contains(set, "Domain") {
			t.Errorf("secure=%v: Set-Cookie carries a Domain: %q", tc.secure, set)
		}

		clear := m.Clear().String()
		for _, want := range []string{tc.name + "=;", "Path=/", "Max-Age=0", "HttpOnly", "SameSite=Lax"} {
			if !strings.Contains(clear, want) {
				t.Errorf("secure=%v: clearing cookie %q lacks %q", tc.secure, clear, want)
			}
		}
		if got := strings.Contains(clear, "; Secure"); got != tc.secure || strings.Contains(clear, "Domain") {
			t.Errorf("secure=%v: clearing cookie %q", tc.secure, clear)
		}

		// The other scheme's name is not this manager's session.
		other := *c
		other.Name = session.CookieName(!tc.secure)
		wantErr(t, m, &other, session.ErrNoSession)
	}
}

func TestSessionIsRenewedInTheSecondHalfOfTheIdleWindow(t *testing.T) {
	clk := newClock()
	t0 := clk.now()
	m := sessiontest.Manager(memStore(t), clk.now)
	c := issue(t, m, "alice")
	if p := payloadOf(t, c); p.IssuedAt != t0.Unix() || p.ExpiresAt != t0.Add(12*time.Hour).Unix() {
		t.Fatalf("issued iat=%d exp=%d", p.IssuedAt, p.ExpiresAt)
	}

	clk.set(t0.Add(6*time.Hour - time.Second)) // first half: no renewal
	if _, renew, err := m.Read(withCookies(c)); err != nil || renew != nil {
		t.Fatalf("at %s: renew=%v err=%v, want no renewal", clk.since(t0), renew, err)
	}

	clk.set(t0.Add(6 * time.Hour)) // second half: renewed
	p, renew, err := m.Read(withCookies(c))
	if err != nil || renew == nil {
		t.Fatalf("at %s: renew=%v err=%v, want a renewal", clk.since(t0), renew, err)
	}
	rp := payloadOf(t, renew)
	if rp.IssuedAt != t0.Unix() || rp.ExpiresAt != clk.now().Add(12*time.Hour).Unix() || rp.ExpiresAt != p.ExpiresAt {
		t.Fatalf("renewed iat=%d exp=%d (returned exp %d), want iat %d exp %d", rp.IssuedAt, rp.ExpiresAt, p.ExpiresAt, t0.Unix(), clk.now().Add(12*time.Hour).Unix())
	}
	if renew.Name != m.CookieName() || renew.MaxAge != int((12*time.Hour)/time.Second) || !renew.HttpOnly || renew.Path != "/" {
		t.Errorf("renewed cookie %q", renew.String())
	}

	clk.set(t0.Add(12 * time.Hour)) // the old cookie is past its idle limit, the renewed one is not
	wantErr(t, m, c, session.ErrExpired)
	wantErr(t, m, renew, nil)
	clk.set(t0.Add(18 * time.Hour))
	wantErr(t, m, renew, session.ErrExpired)
}

func TestSessionDiesAtTheAbsoluteLimit(t *testing.T) {
	clk := newClock()
	t0 := clk.now()
	m := sessiontest.Manager(memStore(t), clk.now)
	c := issue(t, m, "alice")
	limit := t0.Add(7 * 24 * time.Hour)
	// Active every 6 h: renewed each time, until the renewal is capped at iat+7d.
	for clk.set(t0.Add(6 * time.Hour)); clk.now().Before(limit.Add(-6 * time.Hour)); clk.advance(6 * time.Hour) {
		_, renew, err := m.Read(withCookies(c))
		if err != nil || renew == nil {
			t.Fatalf("at %s: renew=%v err=%v", clk.since(t0), renew, err)
		}
		if got, want := payloadOf(t, renew).ExpiresAt, min(clk.now().Add(12*time.Hour).Unix(), limit.Unix()); got != want {
			t.Fatalf("at %s: exp=%d, want %d", clk.since(t0), got, want)
		}
		c = renew
	}
	if exp := payloadOf(t, c).ExpiresAt; exp != limit.Unix() {
		t.Fatalf("last renewal exp=%d, want iat+7d=%d", exp, limit.Unix())
	}
	clk.set(limit.Add(-6 * time.Hour)) // nothing left to extend: no renewal
	if _, renew, err := m.Read(withCookies(c)); err != nil || renew != nil {
		t.Fatalf("at iat+7d-6h: renew=%v err=%v, want valid without renewal", renew, err)
	}
	clk.set(limit.Add(-time.Second))
	wantErr(t, m, c, nil)
	clk.set(limit)
	wantErr(t, m, c, session.ErrExpired)
}

// A payload the manager would never seal (exp beyond iat+7d, no user, exp before iat) is refused
// even under the right key.
func TestReadRefusesAnImpossiblePayload(t *testing.T) {
	clk := newClock()
	m := sessiontest.Manager(memStore(t), clk.now)
	n := clk.now().Unix()
	for name, p := range map[string]session.Payload{
		"exp past iat+7d": {User: "alice", IssuedAt: n, ExpiresAt: n + int64(7*24*time.Hour/time.Second) + 1},
		"no user":         {IssuedAt: n, ExpiresAt: n + 60},
		"exp before iat":  {User: "alice", IssuedAt: n, ExpiresAt: n - 1},
		"no iat":          {User: "alice", ExpiresAt: n + 60},
	} {
		v, err := sessiontest.Codec().Seal(p)
		must(t, err)
		if _, _, err := m.Read(withCookies(&http.Cookie{Name: m.CookieName(), Value: v})); !errors.Is(err, session.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}

func TestTwoSessionCookiesAreRefused(t *testing.T) {
	m := sessiontest.Manager(memStore(t), newClock().now)
	a, b := issue(t, m, "alice"), issue(t, m, "mallory")
	if _, _, err := m.Read(withCookies(b, a)); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("two session cookies: %v, want ErrInvalid", err)
	}
	wantErr(t, m, a, nil)
}

func TestSessionKeepsOnlyListedGroups(t *testing.T) {
	m := sessiontest.Manager(memStore(t), newClock().now)
	c, err := m.Issue(session.Identity{
		User:      "alice",
		Groups:    []string{"everyone", "ops", "dev", "viewers", "ops", "Ops"},
		RoleClaim: []string{"user", "admin", "admin"},
	}, session.Keep{Groups: []string{"viewers", "ops", "unused"}, RoleClaim: []string{"admin"}})
	must(t, err)
	p := payloadOf(t, c)
	if !slices.Equal(p.Groups, []string{"ops", "viewers"}) || !slices.Equal(p.RoleClaim, []string{"admin"}) {
		t.Fatalf("payload g=%q rc=%q, want [ops viewers] [admin]", p.Groups, p.RoleClaim)
	}
	got, _, err := m.Read(withCookies(c))
	must(t, err)
	if !slices.Equal(got.Groups, p.Groups) || !slices.Equal(got.RoleClaim, p.RoleClaim) {
		t.Errorf("Read g=%q rc=%q", got.Groups, got.RoleClaim)
	}

	// Nothing listed: nothing kept.
	c, err = m.Issue(session.Identity{User: "alice", Groups: []string{"ops"}, RoleClaim: []string{"admin"}}, session.Keep{})
	must(t, err)
	if p := payloadOf(t, c); p.Groups != nil || p.RoleClaim != nil {
		t.Errorf("empty Keep kept g=%q rc=%q", p.Groups, p.RoleClaim)
	}
}

func TestSealedCookieOver3500BytesIsRefusedAtLogin(t *testing.T) {
	m := sessiontest.Manager(memStore(t), newClock().now)
	var groups []string
	for i := range 200 {
		groups = append(groups, strings.Repeat("g", 20)+string(rune('a'+i%26))+strings.Repeat("x", i%7)+string(rune('A'+i/26)))
	}
	id := session.Identity{User: "alice", Groups: groups}
	c, err := m.Issue(id, session.Keep{Groups: groups})
	if !errors.Is(err, session.ErrTooLarge) || c != nil {
		t.Fatalf("Issue with %d kept groups: cookie=%v err=%v, want ErrTooLarge", len(groups), c, err)
	}
	// The same identity with only the listed groups kept fits.
	c, err = m.Issue(id, session.Keep{Groups: groups[:3]})
	must(t, err)
	if len(c.Value) > session.MaxSealedBytes {
		t.Fatalf("issued a %d-byte cookie", len(c.Value))
	}
}

func TestUnconfiguredManagerFailsClosed(t *testing.T) {
	clk := newClock()
	good := sessiontest.Manager(memStore(t), clk.now)
	c := issue(t, good, "alice")
	for name, m := range map[string]*session.Manager{
		"no codec":  session.NewManager(session.Options{Epochs: memStore(t), Now: clk.now}),
		"no epochs": session.NewManager(session.Options{Codec: sessiontest.Codec(), Now: clk.now}),
	} {
		if _, err := m.Issue(session.Identity{User: "alice"}, session.Keep{}); err == nil {
			t.Errorf("%s: Issue succeeded", name)
		}
		if _, _, err := m.Read(withCookies(c)); err == nil || errors.Is(err, session.ErrNoSession) {
			t.Errorf("%s: Read = %v, want a failure", name, err)
		}
	}
	if _, err := good.Issue(session.Identity{}, session.Keep{}); err == nil {
		t.Error("Issue accepted an empty user")
	}
}
