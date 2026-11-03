package localauth

import "github.com/TechDufus/dash/internal/credentials"

// Account is the one administrator of auth.mode=local.
type Account struct {
	// User is auth.local.adminUser, the only name a login can succeed for.
	User string
	// EnvHash is the hash DASH_ADMIN_PASSWORD_HASH (or _FILE) supplies; when set the account is
	// env-managed: /setup is 404 and the password cannot be changed from dash.
	EnvHash string
	// Store holds the account created through /setup (and the epochs).
	Store *credentials.Store
}

// EnvManaged reports whether the environment supplies the hash.
func (a Account) EnvManaged() bool { return a.EnvHash != "" }

// Hash is the account's password hash: EnvHash when set, else the stored administrator's. false
// when there is no account yet, and when the stored administrator has another name than User:
// a password set for one name never signs in another (cmd/dash refuses to start in that case).
func (a Account) Hash() (string, bool) {
	if a.EnvHash != "" {
		return a.EnvHash, true
	}
	if a.Store == nil {
		return "", false
	}
	adm, ok := a.Store.Admin()
	if !ok || adm.User != a.User || adm.Hash == "" {
		return "", false
	}
	return adm.Hash, true
}

// Exists reports whether there is an account (from the environment or the store). A stored
// administrator of another name counts: /setup must never replace an account.
func (a Account) Exists() bool {
	if a.EnvHash != "" {
		return true
	}
	if a.Store == nil {
		return false
	}
	_, ok := a.Store.Admin()
	return ok
}

// Local is everything the local mode's routes need. api.Deps carries it in auth.mode=local and
// nil otherwise.
type Local struct {
	Account  Account
	Hasher   *Hasher
	Throttle *Throttle
	// Setup is the token printed at this start; nil when the account existed at startup.
	Setup *SetupToken
}

// SetupNeeded reports whether the local mode is waiting for its administrator: no account exists,
// neither stored nor from the environment. It does not depend on the token: an expired or used
// token leaves setup needed until a restart prints a new one. The 401 envelope's details.setup,
// the /setup page's 404 gate and the anonymous viewer's wait read it; whether POST /setup accepts
// a token is the token's own check (Usable, Consume).
func (l *Local) SetupNeeded() bool {
	return l != nil && !l.Account.Exists()
}
