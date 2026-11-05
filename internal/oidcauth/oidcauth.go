// Package oidcauth is the built-in OIDC login (contract §4.4, P4.4): the authorization code flow
// with PKCE (S256), a state and nonce sealed in a short-lived cookie, ID-token verification
// (signature from the cached JWKS, issuer, audience, expiry, nonce), the claim mapping and the
// fail-closed allow-lists. A successful callback issues a session cookie through
// session.Manager; the session mode's authenticator (internal/session) does the rest. The HTTP
// handlers are in internal/api/authroutes_oidc.go.
//
// Only ID-token claims are used (no UserInfo call). Provider discovery is lazy, with
// DiscoveryTimeout: an unreachable IdP fails /auth/oidc/start with 503 and never stops dash from
// starting.
//
// The exported API names no type of github.com/coreos/go-oidc/v3 or golang.org/x/oauth2.
package oidcauth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
)

// Timings and cookie names.
const (
	// DiscoveryTimeout bounds discovery, the JWKS fetch and the token exchange.
	DiscoveryTimeout = 10 * time.Second
	// StateTTL is the life of the state cookie between /auth/oidc/start and /auth/callback.
	StateTTL = 10 * time.Minute
	// StateCookieSecure is the state cookie's name when server.publicURL is https;
	// StateCookiePlain on http. Both are SameSite=Lax: the callback is a cross-site top-level
	// navigation, which a Strict cookie would not be sent on.
	StateCookieSecure = "__Host-dash_oidc"
	StateCookiePlain  = "dash_oidc"
	// CallbackPath is the fixed redirect path under server.publicURL.
	CallbackPath = "/auth/callback"
)

// Limits of what a login may carry.
const (
	// maxNextBytes bounds the post-login path sealed into the state cookie.
	maxNextBytes = 1024
	// maxConsumed bounds the record of states already used by a callback. When it is full the
	// oldest entry goes: replaying that state also needs its authorization code, which the IdP
	// accepts once.
	maxConsumed = 4096
	// maxErrorDetail bounds the upstream text a Failure carries into the log.
	maxErrorDetail = 200
)

var (
	// ErrInvalidNext is a next that is not a path on this origin (session.ValidNext) or is longer
	// than 1024 bytes: 400 invalid_input.
	ErrInvalidNext = errors.New("oidcauth: invalid next path")
	// ErrUnavailable is a failed discovery at /auth/oidc/start: 503 upstream_unavailable with a
	// generic message. The wrapped error is for the log only.
	ErrUnavailable = errors.New("oidcauth: the identity provider is unavailable")
	// ErrNotAllowed is a callback whose account is on no allow-list: /login?error=not_allowed.
	ErrNotAllowed = errors.New("oidcauth: the account is not on an allow-list")
)

// Failure is a refused callback other than ErrNotAllowed: /login?error=oidc_failed. Stage names
// the step that refused (state_cookie_missing, state_mismatch, exchange, verify, nonce_mismatch,
// claims, session, ...); the text never holds a code, a token, a cookie or the client secret and
// is for the log only.
type Failure struct {
	Stage string
	Err   error
}

func (f *Failure) Error() string {
	if f.Err == nil {
		return "oidc callback refused: " + f.Stage
	}
	return "oidc callback refused: " + f.Stage + ": " + f.Err.Error()
}

func (f *Failure) Unwrap() error { return f.Err }

func fail(stage string, err error) *Failure { return &Failure{Stage: stage, Err: err} }

// Login is the result of a callback: the session cookie to set and the path to go to. User is
// the account the ID token named, set also with ErrNotAllowed (for the audit's target).
type Login struct {
	Cookie *http.Cookie
	Next   string
	User   string
}

// Config is the oidc mode, built by cmd/dash from auth.oidc.* (validated there: the issuer is
// https unless loopback, an allow-list or an explicit "*" is required).
type Config struct {
	Issuer   string
	ClientID string
	// ClientSecret is revealed at each token exchange, so a rotated file applies.
	ClientSecret secret.Value
	Scopes       []string
	// UserClaim names the user (preferred_username by default; email requires email_verified;
	// sub for an IdP whose users can change their name).
	UserClaim string
	// GroupsClaim holds the groups: a list of strings or one comma-separated string.
	GroupsClaim string
	// RoleClaim holds role values matched against Roles' AdminRoles; "" turns it off.
	RoleClaim string
	// AllowedUsers (exact) and AllowedGroups (any) admit an account; "*" in AllowedUsers admits
	// every account the IdP signs in. Anything else is refused: /login?error=not_allowed.
	AllowedUsers  []string
	AllowedGroups []string
	// Roles is the live mapping (auth.oidc adminUsers, adminGroups, adminRoles). The session keeps
	// only the groups of AdminGroups and AllowedGroups and the role values of AdminRoles.
	Roles *auth.RoleHolder
	// RedirectURL is server.publicURL + CallbackPath.
	RedirectURL string
	// Secure is true when server.publicURL is https (the cookie names and Secure attribute).
	Secure bool
	// HTTPClient talks to the IdP; nil = a client with DiscoveryTimeout.
	HTTPClient *http.Client
	Now        func() time.Time // nil = time.Now
	// Secrets cuts an upstream error's text to maxErrorDetail without leaving part of a secret
	// that straddles the cut (secret.Registry.Cut); nil matches the token shapes only.
	Secrets *secret.Registry
}

// Provider is the OIDC login of one configured IdP. Safe for concurrent use.
type Provider struct {
	cfg        Config
	sessions   *session.Manager
	state      *session.Codec
	client     *http.Client
	now        func() time.Time
	issuerHost string

	mu       sync.Mutex // guards disc and inflight
	disc     *discovered
	inflight *flight

	usedMu sync.Mutex // guards used and usedQ
	used   map[string]struct{}
	usedQ  []string // insertion order, for the bound
}

// discovered is what discovery yields; it is cached once it succeeds.
type discovered struct {
	endpoint oauth2.Endpoint
	verifier *oidc.IDTokenVerifier
}

// flight is one discovery in progress: concurrent logins wait for it instead of starting another.
type flight struct {
	done chan struct{}
	d    *discovered
	err  error
}

// flowState is the sealed content of the state cookie.
type flowState struct {
	State    string `json:"s"`
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Next     string `json:"nx"`
	Expires  int64  `json:"exp"` // unix seconds
}

// New returns the provider of cfg. It makes no network call: discovery happens on the first
// login. sessions issues the session cookie; state seals the state cookie (a codec whose key is
// derived for session.PurposeOIDCState).
func New(cfg Config, sessions *session.Manager, state *session.Codec) (*Provider, error) {
	switch {
	case sessions == nil || state == nil:
		return nil, errors.New("oidcauth: a session manager and a state codec are required")
	case cfg.ClientID == "":
		return nil, errors.New("oidcauth: the client ID is required")
	case cfg.RedirectURL == "":
		return nil, errors.New("oidcauth: the redirect URL is required")
	case cfg.UserClaim == "":
		return nil, errors.New("oidcauth: the user claim is required")
	case len(cfg.AllowedUsers) == 0 && len(cfg.AllowedGroups) == 0:
		return nil, errors.New(`oidcauth: allowed users or allowed groups (or an explicit "*") is required`)
	}
	u, err := url.Parse(cfg.Issuer)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errors.New("oidcauth: the issuer must be an http(s) URL")
	}
	if cfg.Roles == nil {
		cfg.Roles = auth.NewRoleHolder(auth.RoleMap{})
	}
	if !slices.Contains(cfg.Scopes, oidc.ScopeOpenID) {
		cfg.Scopes = append([]string{oidc.ScopeOpenID}, cfg.Scopes...)
	}
	p := &Provider{
		cfg: cfg, sessions: sessions, state: state, client: cfg.HTTPClient, now: cfg.Now,
		issuerHost: u.Host, used: make(map[string]struct{}),
	}
	if p.client == nil {
		p.client = &http.Client{Timeout: DiscoveryTimeout}
	}
	if p.now == nil {
		p.now = time.Now
	}
	return p, nil
}

// IssuerHost is the host of the issuer URL, for the 401 envelope's details.oidc.issuer.
func (p *Provider) IssuerHost() string { return p.issuerHost }

// Start begins a login: it validates next ("" is "/"), discovers the provider when it has not yet,
// and returns the IdP's authorization URL (code flow, S256 PKCE challenge, nonce) and the state
// cookie that carries {state, nonce, verifier, next} sealed for StateTTL. ErrInvalidNext for a
// bad next; an error wrapping ErrUnavailable when discovery fails.
func (p *Provider) Start(ctx context.Context, next string) (redirect string, state *http.Cookie, err error) {
	if next == "" {
		next = "/"
	}
	if len(next) > maxNextBytes || !session.ValidNext(next) {
		return "", nil, ErrInvalidNext
	}
	d, err := p.discover(ctx)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	st := flowState{
		State: rand.Text(), Nonce: rand.Text(), Verifier: oauth2.GenerateVerifier(), Next: next,
		Expires: p.now().Add(StateTTL).Unix(),
	}
	sealed, err := p.state.Seal(st)
	if err != nil {
		return "", nil, fmt.Errorf("oidcauth: sealing the state: %w", err)
	}
	loc := p.oauth(d).AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier))
	return loc, p.stateCookie(sealed, int(StateTTL/time.Second)), nil
}

// ClearState is the cookie that deletes the state cookie; the callback sets it in every outcome.
func (p *Provider) ClearState() *http.Cookie { return p.stateCookie("", -1) }

// Callback completes a login from the IdP's redirect to CallbackPath: the state cookie must
// exist, be unexpired and unused, and its state must equal the query's (constant time); the code
// is exchanged with the PKCE verifier; the ID token is verified (signature, issuer, audience,
// expiry) and its nonce must equal the sealed one; the account must be on an allow-list
// (ErrNotAllowed); then a session is issued. Every other refusal is a *Failure. The caller sets
// ClearState in every outcome.
func (p *Provider) Callback(r *http.Request) (Login, error) {
	c, err := r.Cookie(p.cookieName())
	if err != nil || c.Value == "" {
		return Login{}, fail("state_cookie_missing", nil)
	}
	var st flowState
	if err := p.state.Open(c.Value, &st); err != nil {
		return Login{}, fail("state_cookie_invalid", nil)
	}
	if st.State == "" || p.now().Unix() >= st.Expires {
		return Login{}, fail("state_expired", nil)
	}
	q := r.URL.Query()
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		return Login{}, fail("state_mismatch", nil)
	}
	if !p.consume(st.State) {
		return Login{}, fail("state_reused", nil)
	}
	if e := q.Get("error"); e != "" {
		return Login{}, fail("idp_error", errors.New(safeCode(e)))
	}
	code := q.Get("code")
	if code == "" {
		return Login{}, fail("no_code", nil)
	}

	ctx, cancel := context.WithTimeout(r.Context(), DiscoveryTimeout)
	defer cancel()
	d, err := p.discover(ctx)
	if err != nil {
		return Login{}, fail("discovery", err)
	}
	ctx = oidc.ClientContext(ctx, p.client)
	tok, err := p.oauth(d).Exchange(ctx, code, oauth2.VerifierOption(st.Verifier))
	if err != nil {
		return Login{}, fail("exchange", p.exchangeError(err))
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Login{}, fail("no_id_token", nil)
	}
	idt, err := d.verifier.Verify(ctx, raw)
	if err != nil {
		return Login{}, fail("verify", p.truncated(err))
	}
	if idt.Nonce == "" || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(st.Nonce)) != 1 {
		return Login{}, fail("nonce_mismatch", nil)
	}
	var claims map[string]json.RawMessage
	if err := idt.Claims(&claims); err != nil {
		return Login{}, fail("claims", errors.New("the ID token's claims are not a JSON object"))
	}
	id, err := p.identity(claims)
	if err != nil {
		return Login{}, fail("claims", err)
	}
	login := Login{User: id.User, Next: st.Next}
	if !p.allowed(id) {
		return login, ErrNotAllowed
	}
	cookie, err := p.sessions.Issue(id, p.keep())
	if err != nil {
		return Login{User: id.User}, fail("session", err)
	}
	login.Cookie = cookie
	return login, nil
}

// discover returns the cached discovery, or runs one (single flight, DiscoveryTimeout, detached
// from the caller's cancellation so one client going away does not fail the others). A failure is
// not cached: the next login tries again.
func (p *Provider) discover(ctx context.Context) (*discovered, error) {
	p.mu.Lock()
	if p.disc != nil {
		d := p.disc
		p.mu.Unlock()
		return d, nil
	}
	f := p.inflight
	if f == nil {
		f = &flight{done: make(chan struct{})}
		p.inflight = f
		go p.runDiscovery(context.WithoutCancel(ctx), f)
	}
	p.mu.Unlock()
	select {
	case <-f.done:
		return f.d, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *Provider) runDiscovery(ctx context.Context, f *flight) {
	ctx, cancel := context.WithTimeout(ctx, DiscoveryTimeout)
	defer cancel()
	op, err := oidc.NewProvider(oidc.ClientContext(ctx, p.client), p.cfg.Issuer)
	if err == nil {
		f.d = &discovered{
			endpoint: op.Endpoint(),
			verifier: op.Verifier(&oidc.Config{ClientID: p.cfg.ClientID, Now: p.now}),
		}
	} else {
		f.err = p.truncated(err)
	}
	p.mu.Lock()
	if f.err == nil {
		p.disc = f.d
	}
	p.inflight = nil
	p.mu.Unlock()
	close(f.done)
}

// oauth is the code flow's client configuration; the secret is read now, so a rotation applies.
func (p *Provider) oauth(d *discovered) *oauth2.Config {
	return &oauth2.Config{
		ClientID: p.cfg.ClientID, ClientSecret: p.cfg.ClientSecret.Reveal(), Endpoint: d.endpoint,
		RedirectURL: p.cfg.RedirectURL, Scopes: p.cfg.Scopes,
	}
}

func (p *Provider) cookieName() string {
	if p.cfg.Secure {
		return StateCookieSecure
	}
	return StateCookiePlain
}

// stateCookie is the state cookie: HttpOnly, SameSite=Lax (never Strict: the callback is a
// cross-site top-level navigation), Path=/, no Domain, Secure on https.
func (p *Provider) stateCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: p.cookieName(), Value: value, Path: "/", MaxAge: maxAge,
		Secure: p.cfg.Secure, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	}
}

// consume records state as used and reports whether it was unused.
func (p *Provider) consume(state string) bool {
	p.usedMu.Lock()
	defer p.usedMu.Unlock()
	if _, ok := p.used[state]; ok {
		return false
	}
	for len(p.usedQ) >= maxConsumed {
		delete(p.used, p.usedQ[0])
		p.usedQ = p.usedQ[1:]
	}
	p.used[state] = struct{}{}
	p.usedQ = append(p.usedQ, state)
	return true
}

// identity reads the user, the groups and the role-claim values from the ID token's claims.
func (p *Provider) identity(claims map[string]json.RawMessage) (session.Identity, error) {
	var user string
	if err := json.Unmarshal(claims[p.cfg.UserClaim], &user); err != nil || !auth.ValidUserName(user) {
		return session.Identity{}, fmt.Errorf("the %q claim is not a valid user name", p.cfg.UserClaim)
	}
	if p.cfg.UserClaim == "email" {
		var verified bool
		if err := json.Unmarshal(claims["email_verified"], &verified); err != nil || !verified {
			return session.Identity{}, errors.New("the e-mail address is not verified")
		}
	}
	id := session.Identity{User: user}
	if p.cfg.GroupsClaim != "" {
		id.Groups = claimValues(claims[p.cfg.GroupsClaim])
	}
	if p.cfg.RoleClaim != "" {
		id.RoleClaim = claimValues(claims[p.cfg.RoleClaim])
	}
	return id, nil
}

// claimValues reads a claim that is a list of strings or one comma-separated string. Entries
// that are not strings, are empty, or are not valid group names are dropped: they can only ever
// grant less.
func claimValues(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		for _, v := range strings.Split(s, ",") {
			list = append(list, auth.TrimIdentity(v))
		}
	}
	var out []string
	for _, v := range list {
		if s, ok := v.(string); ok && s != "" && auth.ValidGroupName(s) && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// allowed applies the allow-lists: the user exactly (or "*"), or any group.
func (p *Provider) allowed(id session.Identity) bool {
	if slices.Contains(p.cfg.AllowedUsers, "*") || slices.Contains(p.cfg.AllowedUsers, id.User) {
		return true
	}
	for _, g := range id.Groups {
		if slices.Contains(p.cfg.AllowedGroups, g) {
			return true
		}
	}
	return false
}

// keep is what the session may carry, from the mapping as it is now.
func (p *Provider) keep() session.Keep {
	m := p.cfg.Roles.Load()
	groups := slices.Clone(m.AdminGroups)
	for _, g := range p.cfg.AllowedGroups {
		if !slices.Contains(groups, g) {
			groups = append(groups, g)
		}
	}
	return session.Keep{Groups: groups, RoleClaim: m.AdminRoles}
}

// exchangeError is the token endpoint's refusal without its body (the status and the OAuth error
// code only), or the transport error, which names the token URL and no credential.
func (p *Provider) exchangeError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		status := 0
		if re.Response != nil {
			status = re.Response.StatusCode
		}
		return fmt.Errorf("the token endpoint answered %d %s", status, safeCode(re.ErrorCode))
	}
	return p.truncated(err)
}

// safeCode is an OAuth error code fit for a log line: [A-Za-z0-9_.-], at most 64 bytes.
func safeCode(s string) string {
	if s == "" || len(s) > 64 || strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == '-')
	}) {
		return "(unreadable error code)"
	}
	return s
}

// truncated bounds an upstream error's text (discovery puts the response body in it). A secret
// that straddles the cut is redacted whole: the log's scrubber would not match its first half.
func (p *Provider) truncated(err error) error {
	s, cut := p.cfg.Secrets.Cut(err.Error(), maxErrorDetail)
	if !cut {
		return err
	}
	return errors.New(strings.ToValidUTF8(s, "") + "…")
}
