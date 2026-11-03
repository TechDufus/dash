package api

import (
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/credentials"
	"github.com/TechDufus/dash/internal/session"
	"github.com/TechDufus/dash/internal/session/sessiontest"
)

// sessionHarness is what withSessions built, so a test can issue cookies, bump epochs and change
// the mapping between requests.
type sessionHarness struct {
	Store    *credentials.Store
	Sessions *session.Manager
	Auth     *session.Authenticator
	Roles    *auth.RoleHolder
}

// withSessions is a newServer option for a session mode (auth.ModeLocal or auth.ModeOIDC): an
// in-memory credential store, the sessiontest manager over it (fixed key, dash_session, clock
// now) and the session authenticator with roles as Deps.Auth. The local and OIDC route tests add
// Deps.Local or Deps.OIDC with an option of their own.
func withSessions(t *testing.T, mode string, roles auth.RoleMap, anonymous bool, now func() time.Time) (func(*Deps), *sessionHarness) {
	t.Helper()
	store, err := credentials.Open("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := &sessionHarness{Store: store, Sessions: sessiontest.Manager(store, now), Roles: auth.NewRoleHolder(roles)}
	h.Auth = session.NewAuthenticator(session.AuthOptions{Mode: mode, Sessions: h.Sessions, Roles: h.Roles, Anonymous: anonymous})
	return func(d *Deps) {
		d.Auth, d.Sessions, d.Credentials = h.Auth, h.Sessions, h.Store
	}, h
}
