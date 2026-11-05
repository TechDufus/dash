package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/localauth"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
)

const (
	localAdmin    = "admin"
	localPassword = "correct horse battery"
	localPeer     = "127.0.0.1" // peerOf(testPeer)
)

// stubPHC is the stub hasher's "hash": fast, and never the password itself.
func stubPHC(password string) string {
	sum := sha256.Sum256([]byte(password))
	return "stub$" + hex.EncodeToString(sum[:])
}

// recordingHasher is a fast Hasher that records the hash every verify was asked about, so a
// test sees whether a request ran the account's verify, the dummy one, or none. With block set,
// every call parks until release is closed and announces itself on entered first; a call that
// runs while MaxConcurrentHashes others are parked sets overflow instead of parking.
type recordingHasher struct {
	*localauth.Hasher
	block    bool
	entered  chan struct{}
	release  chan struct{}
	inflight atomic.Int32
	overflow atomic.Bool

	mu       sync.Mutex
	verified []string
	hashed   int
}

func newRecordingHasher(block bool) *recordingHasher {
	rh := &recordingHasher{block: block, entered: make(chan struct{}, 8), release: make(chan struct{})}
	park := func() {
		if !rh.block {
			return
		}
		if rh.inflight.Add(1) > localauth.MaxConcurrentHashes {
			rh.overflow.Store(true)
		} else {
			rh.entered <- struct{}{}
			<-rh.release
		}
		rh.inflight.Add(-1)
	}
	rh.Hasher = localauth.NewHasher(localauth.HasherOptions{
		Hash: func(pw string) (string, error) {
			park()
			rh.mu.Lock()
			rh.hashed++
			rh.mu.Unlock()
			return stubPHC(pw), nil
		},
		Verify: func(phc, pw string) (bool, error) {
			park()
			rh.mu.Lock()
			rh.verified = append(rh.verified, phc)
			rh.mu.Unlock()
			return phc == stubPHC(pw), nil
		},
	})
	return rh
}

func (rh *recordingHasher) calls() (verified []string, hashed int) {
	rh.mu.Lock()
	defer rh.mu.Unlock()
	return slices.Clone(rh.verified), rh.hashed
}

// localOpts shape a local-mode server.
type localOpts struct {
	envHash  string            // DASH_ADMIN_PASSWORD_HASH
	storedAs string            // an account stored before the start under this name ("" none)
	setup    bool              // a setup token for this start
	hasher   *localauth.Hasher // nil: a non-blocking recordingHasher
	secrets  *secret.Registry  // Deps.Secrets
}

// localEnv is a local-mode server tested through the handlers' own mux behind s.authn and
// s.csrf, with the route rows the integrator adds: /login, /setup, /auth/logout, and /api/v1/me
// to see what a cookie is worth.
type localEnv struct {
	t        *testing.T
	h        http.Handler
	clock    *auditClock
	sess     *sessionHarness
	local    *localauth.Local
	rec      *recordingHasher
	auditDir string
}

func newLocalEnv(t *testing.T, o localOpts) *localEnv {
	t.Helper()
	c := newAuditClock()
	user := localAdmin
	opt, sh := withSessions(t, auth.ModeLocal, auth.RoleMap{AdminUsers: []string{user}}, false, c.now)
	e := &localEnv{t: t, clock: c, sess: sh, auditDir: t.TempDir()}
	hasher := o.hasher
	if hasher == nil {
		e.rec = newRecordingHasher(false)
		hasher = e.rec.Hasher
	}
	if o.storedAs != "" {
		if err := sh.Store.SetAdmin(credentials.Admin{User: o.storedAs, Hash: stubPHC(localPassword), CreatedAt: c.now()}); err != nil {
			t.Fatal(err)
		}
	}
	e.local = &localauth.Local{
		Account:  localauth.Account{User: user, EnvHash: o.envHash, Store: sh.Store},
		Hasher:   hasher,
		Throttle: localauth.NewThrottle(user, c.now),
	}
	if o.setup {
		tok, err := localauth.NewSetupToken(c.now)
		if err != nil {
			t.Fatal(err)
		}
		e.local.Setup = tok
	}
	al := openAudit(t, e.auditDir, c.now)
	s := newServer(func(d *Deps) {
		opt(d)
		d.Local, d.Audit = e.local, al
		if o.secrets != nil {
			d.Secrets = o.secrets
		}
	})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /setup", s.setup)
	mux.HandleFunc("POST /auth/logout", s.logout)
	mux.HandleFunc("GET /api/v1/me", s.me)
	e.h = s.authn(s.csrf(mux))
	return e
}

func (e *localEnv) login(user, password string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return e.post("/login", map[string]string{"username": user, "password": password}, cookies...)
}

func (e *localEnv) setup(token, password string) *httptest.ResponseRecorder {
	return e.post("/setup", map[string]string{"token": token, "password": password})
}

func (e *localEnv) post(path string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return req{method: http.MethodPost, path: path, body: body, header: cookieHeader(cookies)}.do(e.h)
}

func (e *localEnv) me(c *http.Cookie) *httptest.ResponseRecorder {
	return req{path: "/api/v1/me", header: cookieHeader([]*http.Cookie{c})}.do(e.h)
}

func cookieHeader(cookies []*http.Cookie) map[string]string {
	var parts []string
	for _, c := range cookies {
		if c != nil {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return map[string]string{"Cookie": strings.Join(parts, "; ")}
}

// sessionCookie is the session cookie w set, failing the test without one.
func sessionCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == session.CookiePlain && c.Value != "" && c.MaxAge >= 0 {
			return c
		}
	}
	t.Fatalf("no session cookie in %q", w.Header().Values("Set-Cookie"))
	return nil
}

func wantCode(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d; body %s", w.Code, status, w.Body.String())
	}
	var e model.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || e.Code != code {
		t.Fatalf("body %s, want code %q", w.Body.String(), code)
	}
	if n := len(w.Header().Values("Set-Cookie")); n != 0 {
		t.Fatalf("a refusal sets %d cookies", n)
	}
}

func meUser(t *testing.T, w *httptest.ResponseRecorder) model.Me {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("/api/v1/me: %d %s", w.Code, w.Body.String())
	}
	var m model.Me
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *localEnv) records(action string) []audit.Record {
	var out []audit.Record
	for _, r := range auditRecords(e.t, e.auditDir) {
		if r.Action == action {
			out = append(out, r)
		}
	}
	return out
}

func TestLoginAcceptsOnlyTheConfiguredAccount(t *testing.T) {
	e := newLocalEnv(t, localOpts{storedAs: localAdmin})
	// Each from its own peer, so the per-peer backoff does not end the loop early.
	for i, name := range []string{"Admin", "admin ", " admin", "root", "anonymous", ""} {
		w := req{method: http.MethodPost, path: "/login", peer: "192.0.2." + strconv.Itoa(10+i) + ":1",
			body: map[string]string{"username": name, "password": localPassword}}.do(e.h)
		wantCode(t, w, http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	}
	w := e.login(localAdmin, localPassword)
	if w.Code != http.StatusNoContent {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	c := sessionCookie(t, w)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("cookie attributes %+v", c)
	}
	if m := meUser(t, e.me(c)); m.User != localAdmin || m.Role != string(auth.RoleAdmin) {
		t.Errorf("me = %s %s, want the admin", m.User, m.Role)
	}
	logins := e.records(auditLogin)
	if len(logins) != 1 || logins[0].Actor.User != localAdmin || logins[0].Kind != audit.KindEvent {
		t.Errorf("auth.login records %+v", logins)
	}

	// An account stored under another name than auth.local.adminUser signs in nobody, and setup
	// stays closed: it never replaces an account.
	other := newLocalEnv(t, localOpts{storedAs: "root", setup: true})
	wantCode(t, other.login(localAdmin, localPassword), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	wantCode(t, other.login("root", localPassword), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	wantCode(t, other.setup(other.local.Setup.Value(), localPassword), http.StatusNotFound, model.ErrCodeNotFound)
	if adm, _ := other.sess.Store.Admin(); adm.User != "root" {
		t.Errorf("the stored account became %q", adm.User)
	}
}

func TestUnknownUserAndWrongPasswordLookTheSame(t *testing.T) {
	e := newLocalEnv(t, localOpts{storedAs: localAdmin})
	unknown := e.login("nobody", localPassword)
	wrong := e.login(localAdmin, "not the password")
	short := e.login(localAdmin, "short")

	wantCode(t, unknown, http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	for name, w := range map[string]*httptest.ResponseRecorder{"wrong password": wrong, "short password": short} {
		if w.Code != unknown.Code || w.Body.String() != unknown.Body.String() {
			t.Errorf("%s: %d %s; unknown user: %d %s", name, w.Code, w.Body.String(), unknown.Code, unknown.Body.String())
		}
		if !headersEqual(w.Header(), unknown.Header()) {
			t.Errorf("%s: headers %v; unknown user: %v", name, w.Header(), unknown.Header())
		}
	}
	if msg := decodeErr(t, unknown); msg != "invalid credentials" {
		t.Errorf("message %q", msg)
	}
	// Every attempt ran exactly one verify: the unknown name and the out-of-bounds password
	// against the dummy hash, the wrong password against the account's.
	verified, _ := e.rec.calls()
	stored := stubPHC(localPassword)
	if len(verified) != 3 || verified[0] == stored || verified[1] != stored || verified[2] == stored || verified[0] != verified[2] {
		t.Errorf("verifies %q; want dummy, account, dummy", verified)
	}
}

func headersEqual(a, b http.Header) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !slices.Equal(v, b[k]) {
			return false
		}
	}
	return true
}

func TestWrongPasswordIsRateLimitedAndAudited(t *testing.T) {
	e := newLocalEnv(t, localOpts{storedAs: localAdmin})
	for range localauth.MaxFailures {
		wantCode(t, e.login(localAdmin, "wrong password!"), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	}
	before, _ := e.rec.calls()
	// Refused for a second, even with the right password, without hashing or counting.
	for range 3 {
		w := e.login(localAdmin, localPassword)
		wantCode(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
		if got := w.Header().Get("Retry-After"); got != "1" {
			t.Fatalf("Retry-After %q, want 1", got)
		}
	}
	if after, _ := e.rec.calls(); len(after) != len(before) {
		t.Fatalf("a refused attempt was hashed: %d verifies, want %d", len(after), len(before))
	}
	// Another peer is still refused for the account: the per-user key.
	wantCode(t, req{method: http.MethodPost, path: "/login", peer: "192.0.2.50:1",
		body: map[string]string{"username": localAdmin, "password": localPassword}}.do(e.h),
		http.StatusTooManyRequests, model.ErrCodeRateLimited)

	// The refused attempts were not counted: the next failure doubles the window once.
	e.clock.advance(time.Second)
	wantCode(t, e.login(localAdmin, "wrong password!"), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	if got := e.login(localAdmin, localPassword).Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After after the sixth failure %q, want 2", got)
	}
	e.clock.advance(2 * time.Second)
	if w := e.login(localAdmin, localPassword); w.Code != http.StatusNoContent {
		t.Fatalf("login after the window: %d %s", w.Code, w.Body.String())
	}
	// The success cleared both keys.
	for range localauth.MaxFailures - 1 {
		wantCode(t, e.login(localAdmin, "wrong password!"), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	}
	if w := e.login(localAdmin, localPassword); w.Code != http.StatusNoContent {
		t.Fatalf("the failures before a success still count: %d", w.Code)
	}

	failed := e.records(auditLoginFailed)
	if len(failed) == 0 {
		t.Fatal("no auth.login.failed record")
	}
	reasons := map[string]bool{}
	for _, r := range failed {
		reasons[r.Reason] = true
		if r.Kind != audit.KindDenial || (r.Peer != localPeer && r.Peer != "192.0.2.50") || r.Target.Name != localAdmin || r.Actor.User != "" {
			t.Errorf("failed-login record %+v: want a denial for the peer naming the attempt in the target only", r)
		}
	}
	if !reasons["login_failed"] || !reasons["login_throttled"] {
		t.Errorf("reasons %v, want login_failed and login_throttled", reasons)
	}
	if file := readAuditFile(t, e.auditDir); strings.Contains(file, localPassword) || strings.Contains(file, "wrong password!") || strings.Contains(file, stubPHC(localPassword)) {
		t.Error("the audit log holds a password or a hash")
	}
}

func TestFailedLoginAuditIsRateLimited(t *testing.T) {
	e := newLocalEnv(t, localOpts{storedAs: localAdmin})
	// Three names from one peer within a minute: one record (the names are in the target, not the
	// actor the denial limit keys on).
	for _, name := range []string{"guess-1", "guess-2", "guess-3"} {
		wantCode(t, e.login(name, localPassword), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	}
	if n := len(e.records(auditLoginFailed)); n != 1 {
		t.Fatalf("%d records for three failures from one peer within a minute, want 1", n)
	}
	other := req{method: http.MethodPost, path: "/login", peer: "192.0.2.60:1", body: map[string]string{"username": "guess-4", "password": "x"}}
	wantCode(t, other.do(e.h), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	if n := len(e.records(auditLoginFailed)); n != 2 {
		t.Fatalf("%d records after a second peer failed, want 2", n)
	}
	e.clock.advance(time.Minute + time.Second)
	wantCode(t, e.login("guess-5", localPassword), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	got := e.records(auditLoginFailed)
	if len(got) != 3 || got[2].Target.Name != "guess-5" {
		t.Fatalf("records after a minute %+v, want a third naming guess-5", got)
	}
	// A long attempted name is clipped in the record.
	e.clock.advance(time.Minute + time.Second)
	wantCode(t, e.login(strings.Repeat("n", 1000), localPassword), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	if got := e.records(auditLoginFailed); len(got) != 4 || len(got[3].Target.Name) != maxAuditName {
		t.Errorf("long name recorded as %d bytes, want %d", len(got[len(got)-1].Target.Name), maxAuditName)
	}
}

// within runs f and returns its result, failing the test (instead of hanging it until -timeout)
// when f has not returned after a few seconds: a regression that parks a request the server
// should refuse, or a stream it should never open, then names the step that hung.
func within[T any](t *testing.T, what string, f func() T) T {
	t.Helper()
	done := make(chan T, 1)
	go func() { done <- f() }()
	select {
	case v := <-done:
		return v
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return within 5 s", what)
		var zero T
		return zero
	}
}

func TestThirdConcurrentHashGets429(t *testing.T) {
	rh := newRecordingHasher(true)
	e := newLocalEnv(t, localOpts{setup: true, hasher: rh.Hasher})
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, name := range []string{"first", "second"} {
		wg.Go(func() { codes <- e.login(name, localPassword).Code })
	}
	entered := func() struct{} { return <-rh.entered }
	within(t, "the first login to reach the hasher", entered)
	within(t, "the second login to reach the hasher", entered)

	w := within(t, "a third login", func() *httptest.ResponseRecorder { return e.login(localAdmin, localPassword) })
	wantCode(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if w.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After")
	}
	tok := e.local.Setup.Value()
	wantCode(t, within(t, "a setup beside two logins", func() *httptest.ResponseRecorder { return e.setup(tok, localPassword) }), http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if rh.overflow.Load() {
		t.Fatal("a third hash ran")
	}
	close(rh.release)
	within(t, "the parked logins after the release", func() bool { wg.Wait(); return true })
	close(codes)
	for c := range codes {
		if c != http.StatusUnauthorized {
			t.Errorf("a parked login ended %d, want 401", c)
		}
	}
	// The busy answers burned neither the token nor a throttle count.
	if !e.local.Setup.Usable() {
		t.Fatal("a busy hasher burned the setup token")
	}
	if w := e.setup(tok, localPassword); w.Code != http.StatusNoContent {
		t.Fatalf("setup after the slots came back: %d %s", w.Code, w.Body.String())
	}
}

func TestSetupTokenIsSingleUseAndExpires(t *testing.T) {
	e := newLocalEnv(t, localOpts{setup: true})
	tok := e.local.Setup.Value()
	wantCode(t, e.setup(tok+"x", localPassword), http.StatusUnauthorized, model.ErrCodeInvalidToken)
	wantCode(t, e.setup("", localPassword), http.StatusUnauthorized, model.ErrCodeInvalidToken)
	if _, ok := e.sess.Store.Admin(); ok || !e.local.Setup.Usable() {
		t.Fatal("a wrong token created the account or burned the token")
	}

	w := e.setup(tok, localPassword)
	if w.Code != http.StatusNoContent {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	c := sessionCookie(t, w)
	if m := meUser(t, e.me(c)); m.User != localAdmin || m.Role != string(auth.RoleAdmin) {
		t.Errorf("signed in as %s %s, want the admin", m.User, m.Role)
	}
	adm, ok := e.sess.Store.Admin()
	if !ok || adm.User != localAdmin || adm.Hash != stubPHC(localPassword) || !adm.CreatedAt.Equal(fixedNow) {
		t.Fatalf("stored admin %+v, want admin with the hash of the password", adm)
	}
	if ep := e.sess.Store.Epoch(localAdmin); ep != 1 {
		t.Errorf("epoch of the account %d after setup, want 1", ep)
	}
	if e.local.Setup.Usable() || e.local.SetupNeeded() {
		t.Error("the token is still usable or setup still needed after setup")
	}
	var setups []audit.Record
	for _, r := range e.records(auditSetup) {
		if r.Kind == audit.KindEvent {
			setups = append(setups, r)
		}
	}
	if len(setups) != 1 || setups[0].Actor.User != localAdmin || setups[0].Result != audit.ResultOK {
		t.Errorf("auth.setup events %+v", setups)
	}
	if recs := e.records(auditSetup); len(recs) != 2 || recs[0].Kind != audit.KindDenial || recs[0].Reason != "login_failed" {
		t.Errorf("auth.setup records %+v, want one rate-limited wrong-token denial and the event", recs)
	}
	if file := readAuditFile(t, e.auditDir); strings.Contains(file, tok) || strings.Contains(file, localPassword) {
		t.Error("the audit log holds the token or the password")
	}
	// The account exists now: /setup is gone, the token's second use included.
	wantCode(t, e.setup(tok, "another password"), http.StatusNotFound, model.ErrCodeNotFound)
	if adm2, _ := e.sess.Store.Admin(); adm2.Hash != adm.Hash {
		t.Fatal("a second setup replaced the account")
	}

	// Two requests racing with the right token: one creates the account, the other is refused.
	rh := newRecordingHasher(true)
	race := newLocalEnv(t, localOpts{setup: true, hasher: rh.Hasher})
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, pw := range []string{localPassword, "the other password"} {
		wg.Go(func() { codes <- race.setup(race.local.Setup.Value(), pw).Code })
	}
	<-rh.entered
	<-rh.entered
	close(rh.release)
	wg.Wait()
	close(codes)
	got := map[int]int{}
	for c := range codes {
		got[c]++
	}
	if got[http.StatusNoContent] != 1 || got[http.StatusUnauthorized] != 1 {
		t.Errorf("two setups with one token answered %v, want one 204 and one 401", got)
	}

	// At 1 h + 1 s the token is refused and nothing is stored.
	late := newLocalEnv(t, localOpts{setup: true})
	late.clock.advance(time.Hour + time.Second)
	wantCode(t, late.setup(late.local.Setup.Value(), localPassword), http.StatusUnauthorized, model.ErrCodeInvalidToken)
	if _, ok := late.sess.Store.Admin(); ok {
		t.Error("an expired token created the account")
	}

	// Wrong tokens count against the peer.
	guess := newLocalEnv(t, localOpts{setup: true})
	for range localauth.MaxFailures {
		wantCode(t, guess.setup("dash_setup_guess", localPassword), http.StatusUnauthorized, model.ErrCodeInvalidToken)
	}
	w = guess.setup(guess.local.Setup.Value(), localPassword)
	wantCode(t, w, http.StatusTooManyRequests, model.ErrCodeRateLimited)
	if w.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After %q", w.Header().Get("Retry-After"))
	}
	if failed := guess.records(auditSetup); len(failed) != 2 || failed[0].Reason != "login_failed" || failed[1].Reason != "login_throttled" {
		t.Errorf("setup denials %+v, want one login_failed (rate-limited) and one login_throttled", failed)
	}
}

func TestPasswordLengthBounds(t *testing.T) {
	e := newLocalEnv(t, localOpts{setup: true})
	tok := e.local.Setup.Value()
	for _, pw := range []string{strings.Repeat("a", localauth.MinPasswordBytes-1), strings.Repeat("a", localauth.MaxPasswordBytes+1)} {
		wantCode(t, e.setup(tok, pw), http.StatusBadRequest, model.ErrCodeInvalidInput)
		if strings.Contains(decodeErr(t, e.setup(tok, pw)), pw) {
			t.Error("the error quotes the password")
		}
	}
	if _, hashed := e.rec.calls(); hashed != 0 || !e.local.Setup.Usable() {
		t.Fatalf("a refused password was hashed (%d) or burned the token", hashed)
	}
	if w := e.setup(tok, strings.Repeat("a", localauth.MaxPasswordBytes)); w.Code != http.StatusNoContent {
		t.Fatalf("a 256-byte password: %d %s", w.Code, w.Body.String())
	}

	lower := newLocalEnv(t, localOpts{setup: true})
	if w := lower.setup(lower.local.Setup.Value(), strings.Repeat("a", localauth.MinPasswordBytes)); w.Code != http.StatusNoContent {
		t.Fatalf("a 12-byte password: %d %s", w.Code, w.Body.String())
	}
}

func TestLogoutKillsAStolenCookie(t *testing.T) {
	e := newLocalEnv(t, localOpts{storedAs: localAdmin})
	login := func() *http.Cookie {
		t.Helper()
		w := e.login(localAdmin, localPassword)
		if w.Code != http.StatusNoContent {
			t.Fatalf("login: %d", w.Code)
		}
		return sessionCookie(t, w)
	}
	for _, body := range []any{nil, map[string]string{}, map[string]string{"scope": "this"}, map[string]string{"scope": "user"}} {
		c := login()
		stolen := *c
		meUser(t, e.me(&stolen))
		w := e.post("/auth/logout", body, c)
		if w.Code != http.StatusNoContent {
			t.Fatalf("logout %v: %d %s", body, w.Code, w.Body.String())
		}
		cleared := false
		for _, sc := range w.Result().Cookies() {
			cleared = cleared || (sc.Name == session.CookiePlain && sc.MaxAge < 0)
		}
		if !cleared {
			t.Errorf("logout %v: no clearing cookie in %q", body, w.Header().Values("Set-Cookie"))
		}
		if got := e.me(&stolen); got.Code != http.StatusUnauthorized {
			t.Fatalf("logout %v: the copied cookie still answers %d", body, got.Code)
		}
	}
	if recs := e.records(auditLogout); len(recs) != 4 || recs[0].Actor.User != localAdmin || recs[0].Target.Name != logoutThis || recs[3].Target.Name != logoutUser {
		t.Errorf("auth.logout records %+v", recs)
	}

	// all: an administrator ends every session, a viewer is refused, and so is a caller without one.
	admin := login()
	viewerCookie, err := e.sess.Sessions.Issue(session.Identity{User: "viewer"}, session.Keep{})
	if err != nil {
		t.Fatal(err)
	}
	if m := meUser(t, e.me(viewerCookie)); m.Role != string(auth.RoleViewer) {
		t.Fatalf("the second session is %s", m.Role)
	}
	global := e.sess.Store.GlobalEpoch()
	wantCode(t, e.post("/auth/logout", map[string]string{"scope": "all"}, viewerCookie), http.StatusForbidden, model.ErrCodeForbidden)
	wantCode(t, e.post("/auth/logout", map[string]string{"scope": "all"}), http.StatusForbidden, model.ErrCodeForbidden)
	if e.sess.Store.GlobalEpoch() != global {
		t.Fatal("a refused logout of everyone bumped the global epoch")
	}
	wantCode(t, e.post("/auth/logout", map[string]string{"scope": "everyone"}, admin), http.StatusBadRequest, model.ErrCodeInvalidInput)
	if w := e.post("/auth/logout", map[string]string{"scope": "all"}, admin); w.Code != http.StatusNoContent {
		t.Fatalf("logout all: %d %s", w.Code, w.Body.String())
	}
	if e.sess.Store.GlobalEpoch() != global+1 {
		t.Error("logout all did not bump the global epoch")
	}
	if e.me(admin).Code != http.StatusUnauthorized || e.me(viewerCookie).Code != http.StatusUnauthorized {
		t.Error("a session survived logout all")
	}

	// Without a session, logout only clears the cookie.
	epoch := e.sess.Store.Epoch(localAdmin)
	if w := e.post("/auth/logout", nil); w.Code != http.StatusNoContent || len(w.Header().Values("Set-Cookie")) != 1 {
		t.Errorf("logout without a session: %d %q", w.Code, w.Header().Values("Set-Cookie"))
	}
	if e.sess.Store.Epoch(localAdmin) != epoch {
		t.Error("logout without a session bumped an epoch")
	}
}

func TestEnvHashMakesTheAccountEnvManaged(t *testing.T) {
	hash, err := localauth.HashPassword(localPassword)
	if err != nil {
		t.Fatal(err)
	}
	// The real argon2id hasher end to end, with the hash from the environment.
	e := newLocalEnv(t, localOpts{envHash: hash, setup: true, hasher: localauth.NewHasher(localauth.HasherOptions{})})
	if !e.local.Account.EnvManaged() || e.local.SetupNeeded() {
		t.Fatal("the env hash does not make the account env-managed")
	}
	wantCode(t, e.setup(e.local.Setup.Value(), "a new password!"), http.StatusNotFound, model.ErrCodeNotFound)
	if _, ok := e.sess.Store.Admin(); ok {
		t.Fatal("setup stored an account next to the env hash")
	}
	wantCode(t, e.login(localAdmin, "not the password"), http.StatusUnauthorized, model.ErrCodeUnauthenticated)
	w := e.login(localAdmin, localPassword)
	if w.Code != http.StatusNoContent {
		t.Fatalf("login with the env-managed account: %d %s", w.Code, w.Body.String())
	}
	if m := meUser(t, e.me(sessionCookie(t, w))); m.User != localAdmin {
		t.Errorf("signed in as %q", m.User)
	}
}
