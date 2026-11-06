package main

import (
	"testing"

	"github.com/TechDufus/dash/internal/config"
)

// pushMayNotify, per mode: local admits only the administrator account; oidc admits the users
// of allowedUsers ("*" every one), and everyone once allowedGroups is set (dash cannot check a
// group without a login); proxy admits every named user, never the empty name.
func TestPushMayNotify(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth config.AuthConfig
		yes  []string
		no   []string
	}{
		{"local", config.AuthConfig{Mode: config.AuthModeLocal, Local: config.AuthLocalConfig{AdminUser: "admin"}}, []string{"admin"}, []string{"bob", ""}},
		{"oidc users", config.AuthConfig{Mode: config.AuthModeOIDC, OIDC: config.AuthOIDCConfig{AllowedUsers: []string{"alice"}}}, []string{"alice"}, []string{"bob"}},
		{"oidc wildcard", config.AuthConfig{Mode: config.AuthModeOIDC, OIDC: config.AuthOIDCConfig{AllowedUsers: []string{"*"}}}, []string{"alice", "bob"}, nil},
		{"oidc groups", config.AuthConfig{Mode: config.AuthModeOIDC, OIDC: config.AuthOIDCConfig{AllowedUsers: []string{"alice"}, AllowedGroups: []string{"ops"}}}, []string{"alice", "bob"}, nil},
		{"proxy", config.AuthConfig{Mode: config.AuthModeProxy}, []string{"alice", "bob"}, []string{""}},
	} {
		may := pushMayNotify(tc.auth)
		for _, u := range tc.yes {
			if !may(u) {
				t.Errorf("%s: %q refused", tc.name, u)
			}
		}
		for _, u := range tc.no {
			if may(u) {
				t.Errorf("%s: %q admitted", tc.name, u)
			}
		}
	}
}
