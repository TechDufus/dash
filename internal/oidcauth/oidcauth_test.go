package oidcauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/oidcauth/oidctest"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/session/sessiontest"
)

const testRedirect = "https://dash.example.com" + CallbackPath

func validConfig(issuer string) Config {
	return Config{
		Issuer: issuer, ClientID: "dash-client", UserClaim: "preferred_username",
		AllowedUsers: []string{"alice"}, RedirectURL: testRedirect,
	}
}

func TestNewRefusesAnIncompleteConfig(t *testing.T) {
	sessions, codec := session.NewManager(session.Options{}), &session.Codec{}
	for _, tc := range []struct {
		name string
		mod  func(*Config)
	}{
		{"no allow-list", func(c *Config) { c.AllowedUsers, c.AllowedGroups = nil, nil }},
		{"empty allow-lists", func(c *Config) { c.AllowedUsers, c.AllowedGroups = []string{}, []string{} }},
		{"no client ID", func(c *Config) { c.ClientID = "" }},
		{"no redirect URL", func(c *Config) { c.RedirectURL = "" }},
		{"no user claim", func(c *Config) { c.UserClaim = "" }},
		{"no issuer", func(c *Config) { c.Issuer = "" }},
		{"issuer without a host", func(c *Config) { c.Issuer = "https://" }},
		{"issuer of another scheme", func(c *Config) { c.Issuer = "ftp://idp.example.com" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig("https://idp.example.com")
			tc.mod(&cfg)
			if p, err := New(cfg, sessions, codec); err == nil || p != nil {
				t.Errorf("New = %v, %v; want a refusal", p, err)
			}
		})
	}
	if _, err := New(validConfig("https://idp.example.com"), nil, codec); err == nil {
		t.Error("New without a session manager")
	}
	if _, err := New(validConfig("https://idp.example.com"), sessions, nil); err == nil {
		t.Error("New without a state codec")
	}
	for name, mod := range map[string]func(*Config){
		"users only":    func(c *Config) { c.AllowedGroups = nil },
		"groups only":   func(c *Config) { c.AllowedUsers, c.AllowedGroups = nil, []string{"ops"} },
		"wildcard only": func(c *Config) { c.AllowedUsers = []string{"*"} },
	} {
		cfg := validConfig("https://idp.example.com:8443/realms/main")
		mod(&cfg)
		p, err := New(cfg, sessions, codec)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if p.IssuerHost() != "idp.example.com:8443" {
			t.Errorf("IssuerHost = %q", p.IssuerHost())
		}
		if !slices.Contains(p.cfg.Scopes, "openid") {
			t.Errorf("scopes %v lack openid", p.cfg.Scopes)
		}
	}
}

func TestConsumedStatesAreBounded(t *testing.T) {
	p := &Provider{used: map[string]struct{}{}}
	for i := range maxConsumed {
		if !p.consume(fmt.Sprint("s", i)) {
			t.Fatalf("state %d refused on first use", i)
		}
	}
	if p.consume("s0") || p.consume(fmt.Sprint("s", maxConsumed-1)) {
		t.Fatal("a used state was accepted again")
	}
	// Full: a new state evicts the oldest record and nothing else.
	if !p.consume("new") {
		t.Fatal("a new state was refused when the record was full")
	}
	if len(p.used) != maxConsumed || len(p.usedQ) != maxConsumed {
		t.Errorf("record holds %d/%d, want %d", len(p.used), len(p.usedQ), maxConsumed)
	}
	if p.consume("s1") || p.consume("new") {
		t.Error("only the oldest record may be evicted")
	}
	if !p.consume("s0") {
		t.Error("the oldest record was kept past the bound")
	}
}

// deadlineTransport records, per path, whether the request's context had a deadline and when.
type deadlineTransport struct {
	mu        sync.Mutex
	deadlines map[string][]time.Time
}

func (d *deadlineTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	dl, ok := r.Context().Deadline()
	d.mu.Lock()
	if ok {
		d.deadlines[r.URL.Path] = append(d.deadlines[r.URL.Path], dl)
	} else {
		d.deadlines[r.URL.Path] = append(d.deadlines[r.URL.Path], time.Time{})
	}
	d.mu.Unlock()
	return http.DefaultTransport.RoundTrip(r)
}

// Discovery and the token exchange each run under a deadline of at most DiscoveryTimeout, so a
// hanging IdP cannot hold a login (or a goroutine) open. The bound is checked from above only:
// no assertion depends on how fast the test runs.
func TestDiscoveryAndExchangeAreBoundedByTheTimeout(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC) }
	idp := oidctest.New(t, "dash-client", "s3cret", testRedirect, now)
	dt := &deadlineTransport{deadlines: map[string][]time.Time{}}
	cfg := validConfig(idp.Issuer())
	cfg.ClientSecret, cfg.HTTPClient, cfg.Now = secret.New("s3cret", ""), &http.Client{Transport: dt}, now
	state, err := session.NewCodec(sessiontest.Key())
	if err != nil {
		t.Fatal(err)
	}
	p, err := New(cfg, sessiontest.Manager(epochs{}, now), state)
	if err != nil {
		t.Fatal(err)
	}
	loc, c, err := p.Start(t.Context(), "/")
	if err != nil {
		t.Fatal(err)
	}
	code, st := idp.Authorize(t, loc, oidctest.Grant{Claims: map[string]any{"preferred_username": "alice"}})
	r := httptest.NewRequest(http.MethodGet, CallbackPath+"?code="+code+"&state="+st, nil)
	r.AddCookie(c)
	if _, err := p.Callback(r); err != nil {
		t.Fatal(err)
	}
	latest := time.Now().Add(DiscoveryTimeout)
	dt.mu.Lock()
	defer dt.mu.Unlock()
	for _, path := range []string{oidctest.PathDiscovery, oidctest.PathToken} {
		got := dt.deadlines[path]
		if len(got) != 1 || got[0].IsZero() || got[0].After(latest) {
			t.Errorf("%s deadlines %v, want one no later than %v", path, got, latest)
		}
	}
}

// epochs is an empty epoch store: every user and the global epoch are 0.
type epochs struct{}

func (epochs) Epoch(string) uint64 { return 0 }
func (epochs) GlobalEpoch() uint64 { return 0 }

func TestClaimValues(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want []string
	}{
		{`["ops","dev"]`, []string{"ops", "dev"}},
		{`"ops, dev ,,ops"`, []string{"ops", "dev"}},
		{`"ops"`, []string{"ops"}},
		{`["ops",42,null,"",{"a":1},"dev","ops"]`, []string{"ops", "dev"}},
		{`["has,comma","ok"]`, []string{"ok"}},
		{`["` + strings.Repeat("g", 257) + `","ok"]`, []string{"ok"}},
		{`42`, nil},
		{`{"ops":true}`, nil},
		{`null`, nil},
		{``, nil},
	} {
		if got := claimValues(json.RawMessage(tc.raw)); !slices.Equal(got, tc.want) {
			t.Errorf("claimValues(%s) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestFailureTextIsBounded(t *testing.T) {
	long := errors.New(strings.Repeat("x", 5000))
	if got := truncated(long).Error(); len(got) > maxErrorDetail+len("…") {
		t.Errorf("truncated to %d bytes", len(got))
	}
	short := errors.New("short")
	if truncated(short) != short {
		t.Error("a short error was rewritten")
	}
	for in, want := range map[string]string{
		"invalid_grant": "invalid_grant", "": "(unreadable error code)", "a b": "(unreadable error code)",
		strings.Repeat("a", 65): "(unreadable error code)", "x\ny": "(unreadable error code)",
	} {
		if got := safeCode(in); got != want {
			t.Errorf("safeCode(%q) = %q", in, got)
		}
	}
	f := &Failure{Stage: "verify", Err: short}
	if f.Error() != "oidc callback refused: verify: short" || !errors.Is(f, short) {
		t.Errorf("Failure = %q", f.Error())
	}
}
