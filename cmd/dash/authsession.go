package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/localauth"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
)

// errLocalNeedsState is the refusal of auth.mode=local when the account can neither be created
// (no writable data dir for the setup) nor supplied (no DASH_ADMIN_PASSWORD_HASH).
var errLocalNeedsState = errors.New("auth.mode=local needs a writable data dir (DASH_DATA_DIR) or DASH_ADMIN_PASSWORD_HASH")

// processAuth is the authenticator of the process and, in a session mode, what the API's
// session routes need.
type processAuth struct {
	authn auth.Authenticator
	proxy *auth.Proxy  // proxy mode: the reloader replaces its role map
	sess  *sessionAuth // session modes; nil otherwise
}

// deps hands the authenticator and the session machinery to the API.
func (p processAuth) deps(d *api.Deps) {
	d.Auth = p.authn
	if s := p.sess; s != nil {
		d.Sessions, d.Credentials, d.Local = s.sessions, s.store, s.local
	}
}

// close releases the credential store of a session mode.
func (p processAuth) close() {
	if p.sess != nil {
		_ = p.sess.store.Close()
	}
}

// sessionEnv is what a session mode reads besides the configuration.
type sessionEnv struct {
	adminHash string           // DASH_ADMIN_PASSWORD_HASH[_FILE]; "" when unset
	stderr    io.Writer        // where the setup line is written (os.Stderr)
	now       func() time.Time // nil = time.Now
}

// startAuth builds the authenticator auth.mode selects. Dev and demo outrank the mode
// (buildAuthenticator); the session modes open the credential store and derive the session key.
func startAuth(res *config.Resolved, env sessionEnv, log *slog.Logger) (processAuth, error) {
	if _, fixed := fixedPrincipal(res.Config); !fixed && config.SessionMode(res.Tree.Auth.Mode) {
		s, err := buildSessionAuth(res, env, log)
		if err != nil {
			return processAuth{}, err
		}
		return processAuth{authn: s.authn, sess: s}, nil
	}
	if env.adminHash != "" {
		log.Warn("DASH_ADMIN_PASSWORD_HASH is ignored: dash is not in auth.mode=local")
	}
	a, p, err := buildAuthenticator(res, log)
	return processAuth{authn: a, proxy: p}, err
}

// sessionAuth is the authenticator of a session mode and the machinery of its routes.
type sessionAuth struct {
	authn    *session.Authenticator
	store    *credentials.Store
	sessions *session.Manager
	local    *localauth.Local // auth.mode=local
}

// buildSessionAuth opens the credential store of the data dir (another dash holding it is
// fatal), derives the session key and builds the mode's authenticator. On an error nothing
// stays open.
func buildSessionAuth(res *config.Resolved, env sessionEnv, log *slog.Logger) (_ *sessionAuth, err error) {
	store, err := credentials.Open(res.Config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("credentials store: %w", err)
	}
	defer func() {
		if err != nil {
			_ = store.Close()
		}
	}()
	key, durable, err := sessionKey(res.Keys, store, session.PurposeSession)
	if err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}
	codec, err := session.NewCodec(key)
	if err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}
	s := &sessionAuth{store: store}
	s.sessions = session.NewManager(session.Options{Codec: codec, Epochs: store, Secure: httpsURL(res.Config.PublicURL), Now: env.now})
	if !durable {
		log.Info("auth: sessions end when dash restarts; set DASH_SECRET_KEY and a writable data dir (DASH_DATA_DIR) to keep them")
	}

	a := res.Tree.Auth
	switch a.Mode {
	case config.AuthModeLocal:
		if s.local, err = buildLocal(a.Local, store, env, res.Config.PublicURL, log); err != nil {
			return nil, err
		}
		roles := auth.NewRoleHolder(auth.RoleMap{AdminUsers: []string{a.Local.AdminUser}})
		s.authn = session.NewAuthenticator(session.AuthOptions{Mode: auth.ModeLocal, Sessions: s.sessions, Roles: roles, Anonymous: a.Local.AnonymousViewer})
		log.Info("auth: local mode", "persistent", store.Persistent(), "envManaged", s.local.Account.EnvManaged(),
			"setupOpen", s.local.SetupOpen(), "anonymousViewer", a.Local.AnonymousViewer, "sessionsSurviveRestart", durable)
		if a.Local.AnonymousViewer {
			log.Warn("auth.local.anonymousViewer is on: anyone who can reach dash can read it without signing in (never logs, never actions)")
		}
	default:
		return nil, fmt.Errorf("auth.mode %q is not a session mode", a.Mode)
	}
	return s, nil
}

// buildLocal is the local mode's account and login machinery. The startup rules: a stored
// account under another name than auth.local.adminUser refuses to start (a password set for one
// name never signs in another, and the account must not be silently orphaned); an env hash must
// be argon2id, and a changed one ends the account's sessions; without any account the setup
// token is created and its line written once, unless the store cannot keep what setup creates,
// which refuses to start.
func buildLocal(c config.AuthLocalConfig, store *credentials.Store, env sessionEnv, publicURL string, log *slog.Logger) (*localauth.Local, error) {
	user := c.AdminUser
	if adm, ok := store.Admin(); ok && adm.User != user {
		return nil, fmt.Errorf("auth.local.adminUser is %q, but %s holds the account %q: set auth.local.adminUser back to %q, or delete %s to run setup again (that also ends every session)",
			user, store.Path(), adm.User, adm.User, store.Path())
	}
	if env.adminHash != "" {
		if _, err := localauth.VerifyPassword(env.adminHash, "x"); errors.Is(err, localauth.ErrMalformedHash) {
			return nil, errors.New("DASH_ADMIN_PASSWORD_HASH is not an argon2id hash: generate one with `dash hash-password`")
		}
		changed, err := store.NoteEnvHash(user, env.adminHash)
		if err != nil {
			return nil, fmt.Errorf("credentials store: %w", err)
		}
		if changed {
			log.Info("auth: the administrator's password hash changed in the environment; its sessions have ended")
		}
	}
	l := &localauth.Local{
		Account:  localauth.Account{User: user, EnvHash: env.adminHash, Store: store},
		Hasher:   localauth.NewHasher(localauth.HasherOptions{}),
		Throttle: localauth.NewThrottle(user, env.now),
	}
	if l.Account.Exists() {
		return l, nil
	}
	if !store.Persistent() {
		return nil, errLocalNeedsState
	}
	tok, err := localauth.NewSetupToken(env.now)
	if err != nil {
		return nil, fmt.Errorf("setup token: %w", err)
	}
	l.Setup = tok
	if err := localauth.WriteSetupLine(env.stderr, strings.TrimSuffix(publicURL, "/"), tok); err != nil {
		return nil, fmt.Errorf("setup token: %w", err)
	}
	return l, nil
}

// sessionKey is the key of purpose: derived from DASH_SECRET_KEY and the store's salt when both
// outlive the process, otherwise random for this process (its sessions end with it). durable
// reports which.
func sessionKey(keys *secret.Keyring, store *credentials.Store, purpose string) (key []byte, durable bool, err error) {
	if keys != nil && store.Persistent() {
		k, err := session.DeriveKey(keys, purpose, store.SessionSalt())
		if err == nil {
			return k, true, nil
		}
		if !errors.Is(err, secret.ErrNoKey) {
			return nil, false, err
		}
	}
	k, err := session.RandomKey()
	return k, false, err
}

// httpsURL reports whether the public URL is https: the session cookie is then __Host- and
// Secure.
func httpsURL(publicURL string) bool {
	u, err := url.Parse(publicURL)
	return err == nil && strings.EqualFold(u.Scheme, "https")
}
