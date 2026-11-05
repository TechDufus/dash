package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/oauth"
	"github.com/TechDufus/dash/internal/secret"
)

// oauthLocal is the local mode with OAuth on, in dataDir, with keys (nil: no DASH_SECRET_KEY).
func oauthLocal(dataDir string, keys *secret.Keyring) *config.Resolved {
	return localResolved(dataDir, keys, func(a *config.AuthConfig) { a.OAuth.Enabled = true })
}

// mintAccess runs the authorization flow on pa's server for the local administrator and returns
// an api-audience access token.
func mintAccess(t *testing.T, pa processAuth, res *config.Resolved) string {
	t.Helper()
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type": {"code"}, "client_id": {"dash-android"}, "redirect_uri": {"io.github.techdufus.dash:/oauth2redirect"},
		"state": {"s"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		"resource": {res.Config.PublicURL + "/api"},
	}
	admin := auth.Principal{Kind: auth.KindSession, User: res.Tree.Auth.Local.AdminUser, Role: auth.RoleAdmin}
	ar, err := pa.oauth.Authorize(context.Background(), q, nil, oauth.Caller{User: admin.User})
	if err != nil || ar.ConsentURL == "" {
		t.Fatalf("authorize: %+v, %v", ar, err)
	}
	redirect, err := pa.oauth.Approve(ar.ConsentURL[strings.LastIndex(ar.ConsentURL, "/")+1:], admin, []string{"read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(redirect)
	tr, _, oe := pa.oauth.Token(url.Values{
		"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "client_id": {"dash-android"},
		"redirect_uri": {"io.github.techdufus.dash:/oauth2redirect"}, "code_verifier": {verifier},
	})
	if oe != nil {
		t.Fatal(oe)
	}
	return tr.AccessToken
}

// An access token survives a restart only with DASH_SECRET_KEY: its key is derived from the key
// and the store's salt; without it the key is random per process (the grant and its refresh
// token survive either way).
func TestOAuthAccessTokenAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		keys     *secret.Keyring
		survives bool
	}{
		{"with DASH_SECRET_KEY", testKeyring(t), true},
		{"without", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			res := oauthLocal(dir, tc.keys)
			pa, _, err := startLocal(t, res, hashOf(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			at := mintAccess(t, pa, res)
			if _, err := pa.tokens.Resolve(at, "api"); err != nil {
				t.Fatalf("before the restart: %v", err)
			}
			pa.close()
			pa2, _, err := startLocal(t, oauthLocal(dir, tc.keys), hashOf(t), nil)
			if err != nil {
				t.Fatal(err)
			}
			if n := len(pa2.store.Grants("")); n != 1 {
				t.Errorf("grants after the restart = %d", n)
			}
			_, err = pa2.tokens.Resolve(at, "api")
			if (err == nil) != tc.survives {
				t.Errorf("after the restart: %v, want survives %v", err, tc.survives)
			}
		})
	}
}
