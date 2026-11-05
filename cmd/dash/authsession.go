package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/localauth"
	"github.com/TechDufus/dash/internal/oauth"
	"github.com/TechDufus/dash/internal/oidcauth"
	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/tokens"
)

// errLocalNeedsState is the refusal of auth.mode=local when the account can neither be created
// (no writable data dir for the setup) nor supplied (no DASH_ADMIN_PASSWORD_HASH).
var errLocalNeedsState = errors.New("auth.mode=local needs a writable data dir (DASH_DATA_DIR) or DASH_ADMIN_PASSWORD_HASH")

// processAuth is the authenticator of the process, its credential store and, in a session
// mode, what the API's session routes need.
type processAuth struct {
	authn auth.Authenticator
	proxy *auth.Proxy  // proxy mode: the reloader replaces its role map
	sess  *sessionAuth // session modes; nil otherwise
	// store keeps the personal access tokens (contract D1): the data dir's credentials.yaml in
	// local, oidc and proxy mode, in memory in dev and demo or without a writable data dir; nil
	// in auth.mode=none, which has no tokens.
	store *credentials.Store
	// tokens resolves bearer credentials (contract D2): the store's personal access tokens and
	// auth.tokens; nil in auth.mode=none, like store.
	tokens *tokens.Resolver
	// oauth is the OAuth authorization server (contract D21): auth.oauth.enabled in local, oidc
	// or proxy mode with a credential store that outlives the process; nil otherwise.
	oauth *oauth.Server
	// push says dash sends push notifications (contract D37): a credential store in a writable
	// data dir, DASH_SECRET_KEY and a push contact, in local, oidc or proxy mode.
	push bool
}

// deps hands the authenticator, the token resolver, the credential store and the session
// machinery to the API.
func (p processAuth) deps(d *api.Deps) {
	d.Auth, d.Credentials, d.OAuth, d.PushEnabled = p.authn, p.store, p.oauth, p.push
	if p.tokens != nil { // a nil *Resolver in the interface would not be a nil TokenResolver
		d.Tokens = p.tokens
	}
	if s := p.sess; s != nil {
		d.Sessions, d.Local, d.OIDC = s.sessions, s.local, s.oidc
	}
}

// close writes what the credential store holds in memory and releases it.
func (p processAuth) close() {
	if p.store != nil {
		_ = p.store.Close()
	}
}

// sessionEnv is what a session mode reads besides the configuration.
type sessionEnv struct {
	adminHash string           // DASH_ADMIN_PASSWORD_HASH[_FILE]; "" when unset
	stderr    io.Writer        // where the setup line is written (os.Stderr)
	now       func() time.Time // nil = time.Now
	secrets   *secret.Registry // cuts the IdP's error text (oidcauth.Config.Secrets); nil = shapes only
}

// startAuth builds the authenticator auth.mode selects, opens the credential store and builds
// the token resolver over it. Dev and demo outrank the mode (buildAuthenticator) and keep their
// store in memory; the session modes derive the session key from theirs; auth.mode=none opens
// none and resolves no token. A token owner's role is derived with the mode's live mapping.
func startAuth(res *config.Resolved, env sessionEnv, log *slog.Logger) (processAuth, error) {
	fixedP, fixed := fixedPrincipal(res.Config)
	var pa processAuth
	var roles *auth.RoleHolder
	if !fixed && config.SessionMode(res.Tree.Auth.Mode) {
		s, err := buildSessionAuth(res, env, log)
		if err != nil {
			return processAuth{}, err
		}
		pa, roles = processAuth{authn: s.authn, sess: s, store: s.store}, s.tokenRoles
	} else {
		if env.adminHash != "" {
			log.Warn("DASH_ADMIN_PASSWORD_HASH is ignored: dash is not in auth.mode=local")
		}
		a, p, err := buildAuthenticator(res, log)
		if err != nil {
			return processAuth{}, err
		}
		pa = processAuth{authn: a, proxy: p}
		switch {
		case fixed:
			pa.store, err = credentials.OpenMemory()
			roles = auth.NewRoleHolder(auth.RoleMap{AdminUsers: []string{fixedP.User}})
		case res.Tree.Auth.Mode == config.AuthModeProxy:
			pa.store, err = credentials.Open(res.Config.DataDir)
			roles = p.Roles()
		}
		if err != nil {
			return processAuth{}, fmt.Errorf("credentials store: %w", err)
		}
	}
	if pa.store != nil {
		pa.tokens = tokens.New(pa.store, roles, res.Tree.Auth.Tokens, env.now)
		if n := len(res.Tree.Auth.Tokens); n > 0 {
			log.Info("auth: static tokens", "count", n)
		}
	} else {
		if len(res.Tree.Auth.Tokens) > 0 {
			log.Warn("auth.tokens is ignored: auth.mode=none resolves no bearer token")
		}
		if len(res.Config.CORSOrigins) > 0 {
			log.Warn("server.corsOrigins is ignored: auth.mode=none has no bearer clients, and every other request stays same-origin")
		}
	}
	if oauthOn(res.Tree.Auth, fixed, pa.store, log) {
		srv, err := buildOAuth(res, pa.store)
		if err != nil {
			return processAuth{}, err
		}
		pa.oauth = srv
		pa.tokens.SetOAuth(srv)
	}
	pa.push = pushOn(res, fixed, pa.store, log)
	return pa, nil
}

// buildOAuth is the authorization server over the store. Its access tokens are sealed under a
// key derived from DASH_SECRET_KEY and the store's salt, so they survive a restart; without the
// key they are sealed under a random one and end with the process (the refresh tokens do not).
func buildOAuth(res *config.Resolved, store *credentials.Store) (*oauth.Server, error) {
	key, _, err := sessionKey(res.Keys, store, oauth.AccessPurpose)
	if err != nil {
		return nil, fmt.Errorf("oauth access key: %w", err)
	}
	o := res.Tree.Auth.OAuth
	return oauth.New(oauth.Options{
		PublicURL: res.Config.PublicURL, DirectURL: res.Config.DirectURL, Store: store, AccessKey: key,
		Clients: o.Clients, CIMDAllowPrivate: o.CIMDAllowPrivate,
	})
}

// oauthOn reports whether auth.oauth.enabled can take effect (contract D21): never in dev, demo
// or auth.mode=none, and only with a credential store in a writable data dir, since a grant
// lost at restart would strand its client. An enabled server that cannot run is a WARN.
func oauthOn(a config.AuthConfig, fixed bool, store *credentials.Store, log *slog.Logger) bool {
	if !a.OAuth.Enabled {
		return false
	}
	switch {
	case fixed || a.Mode == config.AuthModeNone:
		log.Warn("auth.oauth.enabled is ignored: dev, demo and auth.mode=none have no OAuth server")
		return false
	case store == nil || !store.Persistent():
		log.Warn("auth.oauth.enabled is ignored: OAuth grants need a writable data dir (DASH_DATA_DIR)")
		return false
	}
	log.Info("auth: OAuth authorization server", "configClients", len(a.OAuth.Clients), "cimdAllowPrivate", a.OAuth.CIMDAllowPrivate)
	return true
}

// pushOn reports whether push can run (contract D37): never in dev, demo or auth.mode=none
// (no outbound traffic from a demo), and otherwise only with a credential store in a writable
// data dir, DASH_SECRET_KEY to seal the VAPID key and endpoints, and a contact for the VAPID
// signature. When it cannot, one INFO names what is missing.
func pushOn(res *config.Resolved, fixed bool, store *credentials.Store, log *slog.Logger) bool {
	var missing []string
	switch {
	case fixed:
		missing = append(missing, "dev and demo mode send no push")
	case res.Tree.Auth.Mode == config.AuthModeNone:
		missing = append(missing, "auth.mode=none has no push")
	default:
		if store == nil || !store.Persistent() {
			missing = append(missing, "a writable data dir (DASH_DATA_DIR)")
		}
		if res.Keys == nil {
			missing = append(missing, "DASH_SECRET_KEY")
		}
		if res.Config.PushContact == "" {
			missing = append(missing, "push.contact (DASH_PUSH_CONTACT), since server.publicURL is not https")
		}
	}
	if len(missing) > 0 {
		log.Info("push: off", "missing", strings.Join(missing, "; "))
		return false
	}
	log.Info("push: on", "allowPrivate", len(res.Tree.Push.AllowPrivate))
	return true
}

// sessionAuth is the authenticator of a session mode and the machinery of its routes.
type sessionAuth struct {
	authn    *session.Authenticator
	store    *credentials.Store
	sessions *session.Manager
	local    *localauth.Local   // auth.mode=local
	oidc     *oidcauth.Provider // auth.mode=oidc
	// roles is the live role map of the oidc mode, which a reload replaces (reloadOIDC); nil in
	// the local mode, whose one account is the administrator.
	roles *auth.RoleHolder
	// tokenRoles is the mapping a personal access token owner's role is derived with: roles in
	// the oidc mode, the one administrator in the local mode.
	tokenRoles *auth.RoleHolder
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
	// The session key is bound to the mode (local and oidc derive different keys), so a cookie of
	// one mode never opens in the other after a switch on the same data dir and DASH_SECRET_KEY:
	// an IdP account named like the local admin, or the local admin past the oidc allow-list.
	key, durable, err := sessionKey(res.Keys, store, session.PurposeSession+"/"+res.Tree.Auth.Mode)
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
		s.tokenRoles = roles
		s.authn = session.NewAuthenticator(session.AuthOptions{Mode: auth.ModeLocal, Sessions: s.sessions, Roles: roles, Anonymous: a.Local.AnonymousViewer, SetupNeeded: s.local.SetupNeeded, OnlyUser: a.Local.AdminUser})
		log.Info("auth: local mode", "persistent", store.Persistent(), "envManaged", s.local.Account.EnvManaged(),
			"setupNeeded", s.local.SetupNeeded(), "anonymousViewer", a.Local.AnonymousViewer, "sessionsSurviveRestart", durable)
		if a.Local.AnonymousViewer {
			log.Warn("auth.local.anonymousViewer is on: anyone who can reach dash can read it without signing in (never logs, never actions)")
		}
	case config.AuthModeOIDC:
		if err := s.buildOIDC(res, env, log); err != nil {
			return nil, err
		}
		s.tokenRoles = s.roles
		log.Info("auth: oidc mode", "issuer", s.oidc.IssuerHost(), "allowedUsers", len(a.OIDC.AllowedUsers), "allowedGroups", len(a.OIDC.AllowedGroups),
			"adminUsers", len(a.OIDC.AdminUsers), "adminGroups", len(a.OIDC.AdminGroups), "adminRoles", len(a.OIDC.AdminRoles), "sessionsSurviveRestart", durable)
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

// oidcOpenWarning is the startup WARN for auth.oidc.allowedUsers: ["*"].
const oidcOpenWarning = `auth.oidc.allowedUsers contains "*": every account the provider signs in may read dash, logs included`

// oidcAdminWildcardWarning is the startup and reload WARN for auth.oidc.adminUsers: ["*"].
const oidcAdminWildcardWarning = `auth.oidc.adminUsers contains "*": every user who may sign in is an administrator`

// buildOIDC is the oidc mode: the provider (discovered on the first login, so an unreachable one
// does not stop the start), a state codec of its own key, and the session authenticator. Both
// share one role holder, which the reload replaces, so a mapping change applies to the next
// request and to what the next login keeps in its cookie.
func (s *sessionAuth) buildOIDC(res *config.Resolved, env sessionEnv, log *slog.Logger) error {
	o := res.Tree.Auth.OIDC
	stateKey, _, err := sessionKey(res.Keys, s.store, session.PurposeOIDCState)
	if err != nil {
		return fmt.Errorf("oidc state key: %w", err)
	}
	state, err := session.NewCodec(stateKey)
	if err != nil {
		return fmt.Errorf("oidc state key: %w", err)
	}
	s.roles = auth.NewRoleHolder(oidcRoleMap(o))
	publicURL := strings.TrimSuffix(res.Config.PublicURL, "/")
	s.oidc, err = oidcauth.New(oidcauth.Config{
		Issuer: o.Issuer, ClientID: o.ClientID, ClientSecret: o.ClientSecret, Scopes: slices.Clone(o.Scopes),
		UserClaim: o.UserClaim, GroupsClaim: o.GroupsClaim, RoleClaim: o.RoleClaim,
		AllowedUsers: cleanNames(o.AllowedUsers), AllowedGroups: cleanNames(o.AllowedGroups), Roles: s.roles,
		RedirectURL: publicURL + oidcauth.CallbackPath, Secure: httpsURL(publicURL), Now: env.now, Secrets: env.secrets,
	}, s.sessions, state)
	if err != nil {
		return err
	}
	var warnings []string
	if slices.Contains(o.AllowedUsers, "*") {
		warnings = append(warnings, auth.WarningOIDCOpen)
		log.Warn(oidcOpenWarning)
	}
	if s.roles.Wildcard() {
		log.Warn(oidcAdminWildcardWarning)
	}
	s.authn = session.NewAuthenticator(session.AuthOptions{Mode: auth.ModeOIDC, Sessions: s.sessions, Roles: s.roles, Warnings: warnings})
	return nil
}

// oidcRoleMap is the role mapping of the oidc settings: users, groups and role values matched
// exactly, cleaned of surrounding space and empty entries.
func oidcRoleMap(o config.AuthOIDCConfig) auth.RoleMap {
	return auth.RoleMap{AdminUsers: cleanNames(o.AdminUsers), AdminGroups: cleanNames(o.AdminGroups), AdminRoles: cleanNames(o.AdminRoles)}
}

// reloadOIDC makes the oidc mapping live: when the auth section changes the next request (and the
// next login's cookie) uses the new adminUsers, adminGroups and adminRoles. h is nil outside the
// oidc mode, which has nothing to reload here.
func (f *framework) reloadOIDC(h *auth.RoleHolder) {
	if h == nil {
		return
	}
	f.reloader.Subscribe(func(c config.Change) {
		if c.Section == "auth" {
			applyOIDCReload(h, f.reloader.Tree().Auth.OIDC)
		}
	})
}

// applyOIDCReload hands the live oidc mapping to the running authenticator.
func applyOIDCReload(h *auth.RoleHolder, o config.AuthOIDCConfig) {
	m := oidcRoleMap(o)
	h.Store(m)
	slog.Info("auth: oidc admin mapping reloaded", "adminUsers", len(m.AdminUsers), "adminGroups", len(m.AdminGroups), "adminRoles", len(m.AdminRoles))
	if m.Wildcard() {
		slog.Warn(oidcAdminWildcardWarning)
	}
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
