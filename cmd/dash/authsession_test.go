package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/localauth"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
)

// localResolved is a resolved configuration in auth.mode=local, changed by mod, with dataDir,
// an https public URL and keys (nil: no DASH_SECRET_KEY).
func localResolved(dataDir string, keys *secret.Keyring, mod func(*config.AuthConfig)) *config.Resolved {
	res := resolvedAuth(false, false, func(a *config.AuthConfig) {
		a.Mode = config.AuthModeLocal
		if mod != nil {
			mod(a)
		}
	})
	res.Config.DataDir, res.Config.PublicURL, res.Keys = dataDir, "https://dash.example.com", keys
	return res
}

// testKeyring is a keyring with a fresh random DASH_SECRET_KEY.
func testKeyring(t *testing.T) *secret.Keyring {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	k, err := secret.ParseKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	return secret.NewKeyring(k, secret.Key{})
}

// realHash is one argon2id hash of a password, computed once: it costs 64 MiB and a fraction of a
// second.
var realHash = sync.OnceValues(func() (string, error) { return localauth.HashPassword("correct horse battery") })

func hashOf(t *testing.T) string {
	t.Helper()
	h, err := realHash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// startLocal is startAuth with the setup line captured; the store is closed at the end of the test.
func startLocal(t *testing.T, res *config.Resolved, adminHash string, logs *logCapture) (processAuth, string, error) {
	t.Helper()
	var stderr bytes.Buffer
	if logs == nil {
		logs = &logCapture{}
	}
	pa, err := startAuth(res, sessionEnv{adminHash: adminHash, stderr: &stderr}, logs.logger())
	if err == nil {
		t.Cleanup(pa.close)
	}
	return pa, stderr.String(), err
}

// sessionRole is what the authenticator makes of a request carrying cookie: the role, or "" when it
// refuses.
func sessionRole(a auth.Authenticator, cookie *http.Cookie) auth.Role {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	r.AddCookie(cookie)
	p, f := a.Authenticate(r)
	if f != nil {
		return ""
	}
	return p.Role
}

// Local mode needs somewhere for its account: a writable data dir (setup creates it) or an env
// hash. With neither, dash refuses to start with the documented message.
func TestLocalModeNeedsAWritableDirOrAnEnvHash(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	_, line, err := startLocal(t, localResolved(missing, nil, nil), "", nil)
	if !errors.Is(err, errLocalNeedsState) || !strings.Contains(err.Error(), "auth.mode=local needs a writable data dir (DASH_DATA_DIR) or DASH_ADMIN_PASSWORD_HASH") || line != "" {
		t.Fatalf("no dir, no hash: err %v, line %q; want the refusal and no setup line", err, line)
	}

	pa, line, err := startLocal(t, localResolved(missing, nil, nil), hashOf(t), nil)
	if err != nil || line != "" || pa.sess == nil || !pa.sess.local.Account.EnvManaged() || pa.sess.local.SetupNeeded() {
		t.Fatalf("env hash without a dir: %v, line %q; want an env-managed account and no setup", err, line)
	}

	pa, line, err = startLocal(t, localResolved(t.TempDir(), nil, nil), "", nil)
	if err != nil || !pa.sess.local.SetupNeeded() || !strings.Contains(line, "setup token") || !strings.Contains(line, "https://dash.example.com/setup#token=dash_setup_") {
		t.Fatalf("writable dir: %v, line %q; want setup needed and the setup line", err, line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Errorf("setup line written %d times, want once:\n%s", strings.Count(line, "\n"), line)
	}

	if _, _, err := startLocal(t, localResolved(missing, nil, nil), "$2y$10$notargon", nil); err == nil || !strings.Contains(err.Error(), "dash hash-password") || strings.Contains(err.Error(), "notargon") {
		t.Errorf("malformed env hash: %v, want a refusal naming dash hash-password and not the hash", err)
	}
}

// Sessions survive a restart only with DASH_SECRET_KEY and a writable data dir; without either the
// key is random per process and every session ends with it.
func TestRandomSessionKeyWithoutSecretKeyOrDataDir(t *testing.T) {
	keys := testKeyring(t)
	for _, tc := range []struct {
		name     string
		dir      func() string
		keys     *secret.Keyring
		survives bool
	}{
		{"key and dir", t.TempDir, keys, true},
		{"dir, no key", t.TempDir, nil, false},
		{"key, no dir", func() string { return filepath.Join(t.TempDir(), "absent") }, keys, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tc.dir()
			var logs logCapture
			first, _, err := startLocal(t, localResolved(dir, tc.keys, nil), hashOf(t), &logs)
			if err != nil {
				t.Fatal(err)
			}
			cookie, err := first.sess.sessions.Issue(session.Identity{User: "admin"}, session.Keep{})
			if err != nil {
				t.Fatal(err)
			}
			if sessionRole(first.authn, cookie) != auth.RoleAdmin {
				t.Fatal("the cookie does not work in the process that issued it")
			}
			if notice := len(logs.with(t, "INFO", "auth: sessions end when dash restarts; set DASH_SECRET_KEY and a writable data dir (DASH_DATA_DIR) to keep them")); (notice == 0) != tc.survives {
				t.Errorf("restart notice logged %d times, survives %v", notice, tc.survives)
			}
			first.close()
			again, _, err := startLocal(t, localResolved(dir, tc.keys, nil), hashOf(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := sessionRole(again.authn, cookie) == auth.RoleAdmin; got != tc.survives {
				t.Errorf("after a restart the session works = %v, want %v", got, tc.survives)
			}
		})
	}
}

// A changed DASH_ADMIN_PASSWORD_HASH ends the account's sessions at the next start; the same one
// does not.
func TestEnvHashChangeEndsSessionsAtStart(t *testing.T) {
	dir, keys := t.TempDir(), testKeyring(t)
	first, _, err := startLocal(t, localResolved(dir, keys, nil), hashOf(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	cookie, err := first.sess.sessions.Issue(session.Identity{User: "admin"}, session.Keep{})
	if err != nil {
		t.Fatal(err)
	}
	first.close()
	same, _, err := startLocal(t, localResolved(dir, keys, nil), hashOf(t), nil)
	if err != nil || sessionRole(same.authn, cookie) != auth.RoleAdmin {
		t.Fatalf("same hash: %v; the session must survive", err)
	}
	same.close()
	other, err := localauth.HashPassword("another long password")
	if err != nil {
		t.Fatal(err)
	}
	var logs logCapture
	changed, _, err := startLocal(t, localResolved(dir, keys, nil), other, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if sessionRole(changed.authn, cookie) != "" {
		t.Error("the session survived a changed env hash")
	}
	if len(logs.with(t, "INFO", "auth: the administrator's password hash changed in the environment; its sessions have ended")) != 1 {
		t.Error("the change was not logged")
	}
}

// A stored account under another name than auth.local.adminUser refuses to start, env hash or
// not, naming both names and both ways out; under its own name it starts.
func TestLocalAdminNameChangeRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	store, err := credentials.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAdmin(credentials.Admin{User: "alice", Hash: hashOf(t), CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, envHash := range []string{"", hashOf(t)} {
		_, _, err := startLocal(t, localResolved(dir, nil, nil), envHash, nil)
		if err == nil {
			t.Fatalf("env hash %v: started with a renamed account", envHash != "")
		}
		for _, want := range []string{`auth.local.adminUser is "admin"`, `holds the account "alice"`, `set auth.local.adminUser back to "alice"`, "delete " + filepath.Join(dir, credentials.FileName), "ends every session"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("env hash %v: error %q lacks %q", envHash != "", err, want)
			}
		}
	}
	pa, line, err := startLocal(t, localResolved(dir, nil, func(a *config.AuthConfig) { a.Local.AdminUser = "alice" }), "", nil)
	if err != nil || line != "" || pa.sess.local.SetupNeeded() {
		t.Errorf("the account's own name: %v, line %q; want a start without setup", err, line)
	}
}

// The anonymous viewer is off unless configured: without a session a read is 401; with it on, a
// read is the anonymous viewer and a write still 401. It waits for the account: while setup is
// needed a read is 401, so the SPA points to the setup link.
func TestAnonymousViewerIsOffByDefaultAtStart(t *testing.T) {
	if config.DefaultAuth().Local.AnonymousViewer {
		t.Fatal("auth.local.anonymousViewer defaults to on")
	}
	off, _, err := startLocal(t, localResolved(t.TempDir(), nil, nil), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, f := off.authn.Authenticate(httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)); f == nil || f.Status != http.StatusUnauthorized {
		t.Errorf("default: an unauthenticated read = %+v, want 401", f)
	}
	var logs logCapture
	pending, line, err := startLocal(t, localResolved(t.TempDir(), nil, func(a *config.AuthConfig) { a.Local.AnonymousViewer = true }), "", &logs)
	if err != nil || line == "" {
		t.Fatalf("setup pending: %v, line %q", err, line)
	}
	if _, f := pending.authn.Authenticate(httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)); f == nil || f.Status != http.StatusUnauthorized {
		t.Errorf("anonymous read while setup is needed = %+v, want 401", f)
	}
	on, _, err := startLocal(t, localResolved(t.TempDir(), nil, func(a *config.AuthConfig) { a.Local.AnonymousViewer = true }), hashOf(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, f := on.authn.Authenticate(httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)); f != nil || p.Kind != auth.KindAnonymous || p.Role != auth.RoleViewer {
		t.Errorf("anonymous read = %+v %+v, want the anonymous viewer", p, f)
	}
	if _, f := on.authn.Authenticate(httptest.NewRequest(http.MethodPost, "/api/v1/plugins/x/actions/y", nil)); f == nil || f.Status != http.StatusUnauthorized {
		t.Errorf("anonymous write = %+v, want 401", f)
	}
	if len(logs.with(t, "WARN", "")) == 0 {
		t.Error("the anonymous viewer is on without a WARN")
	}
}

// The proxy, none, dev and demo modes open no credential store and ignore an admin hash, with a
// WARN.
func TestOtherModesOpenNoCredentialStore(t *testing.T) {
	dir := t.TempDir()
	res := resolvedAuth(false, false, nil)
	res.Config.DataDir = dir
	var logs logCapture
	pa, _, err := startLocal(t, res, hashOf(t), &logs)
	if err != nil || pa.sess != nil || pa.proxy == nil {
		t.Fatalf("proxy mode: %+v %v", pa, err)
	}
	if len(logs.with(t, "WARN", "DASH_ADMIN_PASSWORD_HASH is ignored: dash is not in auth.mode=local")) != 1 {
		t.Error("the ignored hash was not warned about")
	}
	dev := localResolved(dir, nil, nil)
	dev.Config.Dev = true
	if pa, _, err := startLocal(t, dev, "", nil); err != nil || pa.sess != nil {
		t.Errorf("dev mode with auth.mode=local: %+v %v, want the fixed identity", pa, err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "*credentials*")); len(m) != 0 {
		t.Errorf("a credential file was created outside a session mode: %v", m)
	}
}

// oidcIn is localResolved switched to auth.mode=oidc on dataDir and keys, with allowed as the
// allow-list (the issuer is never contacted: dash asks it at the first login, not at start).
func oidcIn(dataDir string, keys *secret.Keyring, allowed ...string) *config.Resolved {
	return localResolved(dataDir, keys, func(a *config.AuthConfig) {
		a.Mode = config.AuthModeOIDC
		a.OIDC.Issuer, a.OIDC.ClientID = "https://127.0.0.1:1/realms/none", "dash"
		a.OIDC.ClientSecret = secret.New("client-secret-value", "")
		a.OIDC.AllowedUsers = allowed
	})
}

// A session never survives a switch between the session modes on one data dir and one
// DASH_SECRET_KEY: the session key is bound to the mode. Otherwise an IdP account named like the
// local admin would be the local admin after oidc → local, and the local admin's cookie would
// pass the oidc allow-list after local → oidc. In the local mode a session of any other user than
// auth.local.adminUser is refused, never a viewer. (From the v0.12.0 security verification.)
func TestSessionsDoNotSurviveAModeSwitch(t *testing.T) {
	dir, keys := t.TempDir(), testKeyring(t)
	o, _, err := startLocal(t, oidcIn(dir, keys, "*"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// What the callback issues for an IdP account whose preferred_username is "admin".
	idpAdmin, err := o.sess.sessions.Issue(session.Identity{User: "admin"}, session.Keep{})
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRole(o.authn, idpAdmin); got != auth.RoleViewer {
		t.Fatalf("in oidc mode the IdP's admin is %q, want viewer", got)
	}
	o.close()

	l, _, err := startLocal(t, localResolved(dir, keys, nil), hashOf(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRole(l.authn, idpAdmin); got != "" {
		t.Errorf("oidc → local: the oidc session of %q is %q in local mode, want refused", "admin", got)
	}
	localAdmin, err := l.sess.sessions.Issue(session.Identity{User: "admin"}, session.Keep{})
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRole(l.authn, localAdmin); got != auth.RoleAdmin {
		t.Fatalf("the local admin's own session is %q, want admin", got)
	}
	other, err := l.sess.sessions.Issue(session.Identity{User: "bob"}, session.Keep{})
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRole(l.authn, other); got != "" {
		t.Errorf("a local session of another user than adminUser is %q, want refused", got)
	}
	l.close()

	o2, _, err := startLocal(t, oidcIn(dir, keys, "alice"), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRole(o2.authn, localAdmin); got != "" {
		t.Errorf("local → oidc: the local admin's session is %q with allowedUsers [alice], want refused", got)
	}
}

// Deleting credentials.yaml while dash runs: the next write (any epoch bump) does not bring the
// file back with the old salt, and after a restart the session from before the deletion is
// refused. (From the v0.12.0 security verification.)
func TestCredentialsDeletedWhileRunningStayDeleted(t *testing.T) {
	dir, keys := t.TempDir(), testKeyring(t)
	first, _, err := startLocal(t, localResolved(dir, keys, nil), hashOf(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	c, err := first.sess.sessions.Issue(session.Identity{User: "admin"}, session.Keep{})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, credentials.FileName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := first.sess.store.BumpEpoch("someone"); err != nil {
		t.Fatalf("an epoch bump after the deletion: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("credentials.yaml came back after a write: %v", err)
	}
	first.close()
	again, _, err := startLocal(t, localResolved(dir, keys, nil), hashOf(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionRole(again.authn, c); got != "" {
		t.Errorf("after the deletion and a restart the old session is %q, want refused", got)
	}
}
