package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

// authProblems is what AuthConfig.Validate reports for a default section changed by mutate,
// paths relative to "auth" as the section reports them.
func authProblems(mutate func(*AuthConfig)) []pluginconf.FieldError {
	a := DefaultAuth()
	mutate(&a)
	return pluginconf.Problems(a.Validate())
}

// hasProblem reports whether a problem sits at path and its message holds every part.
func hasProblem(ps []pluginconf.FieldError, path string, parts ...string) bool {
next:
	for _, p := range ps {
		if p.Path != path {
			continue
		}
		for _, part := range parts {
			if !strings.Contains(p.Msg, part) {
				continue next
			}
		}
		return true
	}
	return false
}

// No env and no file: the local mode (v0.12.0), and for the proxy mode the two user headers,
// groups off, the loopback believed and nobody an administrator. The expectation is written out,
// not derived from DefaultAuth.
func TestAuthDefaults(t *testing.T) {
	res, _, err := resolveEnv(t)
	if err != nil {
		t.Fatal(err)
	}
	a := res.Tree.Auth
	if a.Mode != "local" {
		t.Errorf("auth.mode = %q, want local", a.Mode)
	}
	p := a.Proxy
	if want := []string{"X-Forwarded-Preferred-Username", "X-Forwarded-User"}; !reflect.DeepEqual(p.UserHeaders, want) {
		t.Errorf("auth.proxy.userHeaders = %q, want %q", p.UserHeaders, want)
	}
	if p.GroupsHeader != "" {
		t.Errorf("auth.proxy.groupsHeader = %q, want empty (groups off)", p.GroupsHeader)
	}
	if want := []string{"127.0.0.1/32", "::1/128"}; !reflect.DeepEqual(p.TrustedProxies, want) {
		t.Errorf("auth.proxy.trustedProxies = %q, want %q", p.TrustedProxies, want)
	}
	if p.AllowAnyPeer {
		t.Error("auth.proxy.allowAnyPeer is on by default")
	}
	if len(p.AdminUsers) != 0 || len(p.AdminGroups) != 0 {
		t.Errorf("admin lists = %q / %q, want both empty (every user is a viewer)", p.AdminUsers, p.AdminGroups)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("the defaults are invalid: %v", err)
	}
}

// The environment names of the auth section and whether a restart applies each: pinned through the
// real schema, then each variable set through the real environment layer lands in its key.
func TestAuthSectionEnvNames(t *testing.T) {
	byEnv := map[string]EnvVar{}
	for _, v := range EnvVars() {
		byEnv[v.Name] = v
	}
	type row struct {
		path, typ, def string
		restart        bool
		set            string // a valid value of the variable
		want           any    // what the tree holds after setting it
	}
	rows := map[string]row{
		"DASH_AUTH_MODE":                   {"auth.mode", "string", "local", true, "proxy", "proxy"},
		"DASH_AUTH_PROXY_USER_HEADERS":     {"auth.proxy.userHeaders", "list of strings (JSON)", `["X-Forwarded-Preferred-Username","X-Forwarded-User"]`, true, `["X-Email"]`, []string{"X-Email"}},
		"DASH_AUTH_PROXY_GROUPS_HEADER":    {"auth.proxy.groupsHeader", "string", "", true, "X-Forwarded-Groups", "X-Forwarded-Groups"},
		"DASH_AUTH_PROXY_TRUSTED_PROXIES":  {"auth.proxy.trustedProxies", "list of strings (JSON)", `["127.0.0.1/32","::1/128"]`, true, `["10.0.0.0/8"]`, []string{"10.0.0.0/8"}},
		"DASH_AUTH_PROXY_ALLOW_ANY_PEER":   {"auth.proxy.allowAnyPeer", "boolean", "", true, "true", true},
		"DASH_AUTH_PROXY_ADMIN_USERS":      {"auth.proxy.adminUsers", "list of strings (JSON)", "", false, `["alice"]`, []string{"alice"}},
		"DASH_AUTH_PROXY_ADMIN_GROUPS":     {"auth.proxy.adminGroups", "list of strings (JSON)", "", false, `["ops"]`, []string{"ops"}},
		"DASH_AUTH_LOCAL_ADMIN_USER":       {"auth.local.adminUser", "string", "admin", true, "root", "root"},
		"DASH_AUTH_LOCAL_ANONYMOUS_VIEWER": {"auth.local.anonymousViewer", "boolean", "", true, "true", true},
		"DASH_AUTH_OIDC_ISSUER":            {"auth.oidc.issuer", "string", "", true, "https://login.example.com/realms/home", "https://login.example.com/realms/home"},
		"DASH_AUTH_OIDC_CLIENT_ID":         {"auth.oidc.clientID", "string", "", true, "dash", "dash"},
		"DASH_AUTH_OIDC_CLIENT_SECRET":     {"auth.oidc.clientSecret", "string", "", true, "s3cret-value", "s3cret-value"},
		"DASH_AUTH_OIDC_SCOPES":            {"auth.oidc.scopes", "list of strings (JSON)", `["openid","profile","email"]`, true, `["openid","groups"]`, []string{"openid", "groups"}},
		"DASH_AUTH_OIDC_USER_CLAIM":        {"auth.oidc.userClaim", "string", "preferred_username", true, "sub", "sub"},
		"DASH_AUTH_OIDC_GROUPS_CLAIM":      {"auth.oidc.groupsClaim", "string", "groups", true, "roles", "roles"},
		"DASH_AUTH_OIDC_ROLE_CLAIM":        {"auth.oidc.roleClaim", "string", "", true, "realm_roles", "realm_roles"},
		"DASH_AUTH_OIDC_ADMIN_ROLES":       {"auth.oidc.adminRoles", "list of strings (JSON)", `["admin"]`, false, `["dash-admin"]`, []string{"dash-admin"}},
		"DASH_AUTH_OIDC_ALLOWED_USERS":     {"auth.oidc.allowedUsers", "list of strings (JSON)", "", true, `["alice"]`, []string{"alice"}},
		"DASH_AUTH_OIDC_ALLOWED_GROUPS":    {"auth.oidc.allowedGroups", "list of strings (JSON)", "", true, `["family"]`, []string{"family"}},
		"DASH_AUTH_OIDC_ADMIN_USERS":       {"auth.oidc.adminUsers", "list of strings (JSON)", "", false, `["alice"]`, []string{"alice"}},
		"DASH_AUTH_OIDC_ADMIN_GROUPS":      {"auth.oidc.adminGroups", "list of strings (JSON)", "", false, `["ops"]`, []string{"ops"}},
	}
	// Nothing else is generated under auth: a key added without its row fails here.
	for name, v := range byEnv {
		if strings.HasPrefix(v.Path, "auth.") {
			if _, ok := rows[name]; !ok {
				t.Errorf("%s (%s) has no row in this test", name, v.Path)
			}
		}
	}
	for name, want := range rows {
		got, ok := byEnv[name]
		if !ok {
			t.Errorf("%s is not a setting", name)
			continue
		}
		have, wantCols := [4]any{got.Path, got.Type, got.Default, got.Restart}, [4]any{want.path, want.typ, want.def, want.restart}
		if have != wantCols {
			t.Errorf("%s = %v, want %v", name, have, wantCols)
		}
		if got.Secret != (name == "DASH_AUTH_OIDC_CLIENT_SECRET") {
			t.Errorf("%s: secret = %v", name, got.Secret)
		}
		if got.Description == "" {
			t.Errorf("%s has no description", name)
		}
		env := []string{name + "=" + want.set}
		if name == "DASH_AUTH_PROXY_ADMIN_GROUPS" {
			// adminGroups is only valid beside a groups header.
			env = append(env, "DASH_AUTH_PROXY_GROUPS_HEADER=X-Forwarded-Groups")
		}
		if name == "DASH_AUTH_LOCAL_ANONYMOUS_VIEWER" {
			// The anonymous viewer exists only in the local mode.
			env = append(env, "DASH_AUTH_MODE=local")
		}
		res, _, err := resolveEnv(t, env...)
		if err != nil {
			t.Errorf("%s=%s: %v", name, want.set, err)
			continue
		}
		if have := value(t, res, want.path); !reflect.DeepEqual(have, want.want) {
			t.Errorf("%s=%s sets %s to %#v, want %#v", name, want.set, want.path, have, want.want)
		}
	}
}

// The mode is an enum and nothing outside it starts: an unknown value fails startup naming the
// key and the variable that set it.
func TestAuthModeEnumRejectsUnknown(t *testing.T) {
	for _, mode := range []string{"OIDC", "Proxy", "ldap", "none-of-them"} {
		_, _, err := resolveEnv(t, "DASH_AUTH_MODE="+mode)
		if err == nil {
			t.Errorf("auth.mode %q was accepted", mode)
			continue
		}
		for _, w := range []string{"auth.mode", "(env DASH_AUTH_MODE)", "proxy"} {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("auth.mode %q: error %q does not contain %q", mode, err, w)
			}
		}
	}
	if ps := authProblems(func(a *AuthConfig) { a.Mode = "" }); !hasProblem(ps, "mode", "not an auth mode") {
		t.Errorf("an empty mode is accepted: %+v", ps)
	}
	file := &File{Path: "c.yaml", Data: []byte("auth: {mode: ldap}\n")}
	if _, _, err := resolveWith(t, Sources{Environ: []string{"DASH_DEMO=true"}, File: file}); err == nil || !strings.Contains(err.Error(), "(file c.yaml)") {
		t.Errorf("a file-set mode: error %v, want one naming the file", err)
	}
}

// No peer could sign in with an empty list, so it is refused at startup instead of locking
// everyone out with 401s; the message says what to set.
func TestEmptyTrustedProxiesIsAConfigError(t *testing.T) {
	for name, set := range map[string]func(*AuthConfig){
		"nil":   func(a *AuthConfig) { a.Proxy.TrustedProxies = nil },
		"empty": func(a *AuthConfig) { a.Proxy.TrustedProxies = []string{} },
	} {
		if ps := authProblems(set); !hasProblem(ps, "proxy.trustedProxies", "at least one") {
			t.Errorf("%s list: problems %+v", name, ps)
		}
	}
	_, _, err := resolveEnv(t, "DASH_AUTH_PROXY_TRUSTED_PROXIES=[]")
	if err == nil {
		t.Fatal("an empty DASH_AUTH_PROXY_TRUSTED_PROXIES was accepted")
	}
	for _, w := range []string{"auth.proxy.trustedProxies", "(env DASH_AUTH_PROXY_TRUSTED_PROXIES)", "at least one"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
}

// 0.0.0.0/0 and ::/0 let every client forge an identity: an error unless allowAnyPeer says the
// operator means it. A wide-but-not-total prefix is not that.
func TestAnyPeerPrefixNeedsTheOptIn(t *testing.T) {
	for _, list := range [][]string{
		{"0.0.0.0/0"},
		{"::/0"},
		{"127.0.0.1/32", "0.0.0.0/0"},
		{" ::/0 "},
		{"0.0.0.0/0", "::/0"},
	} {
		ps := authProblems(func(a *AuthConfig) { a.Proxy.TrustedProxies = list })
		if !hasProblem(ps, "proxy.trustedProxies", "allowAnyPeer") {
			t.Errorf("%q without the opt-in: problems %+v", list, ps)
		}
		ps = authProblems(func(a *AuthConfig) { a.Proxy.TrustedProxies = list; a.Proxy.AllowAnyPeer = true })
		if len(ps) != 0 {
			t.Errorf("%q with allowAnyPeer: problems %+v", list, ps)
		}
	}
	for _, list := range [][]string{{"0.0.0.0/1"}, {"10.0.0.0/8"}, {"::/1"}, {"192.0.2.1"}} {
		if ps := authProblems(func(a *AuthConfig) { a.Proxy.TrustedProxies = list }); len(ps) != 0 {
			t.Errorf("%q is not a whole family: problems %+v", list, ps)
		}
	}

	// Through the real layers: the opt-in is a separate setting, not a spelling of the list.
	if _, _, err := resolveEnv(t, `DASH_AUTH_PROXY_TRUSTED_PROXIES=["0.0.0.0/0"]`); err == nil ||
		!strings.Contains(err.Error(), "auth.proxy.trustedProxies") || !strings.Contains(err.Error(), "allowAnyPeer") {
		t.Errorf("0.0.0.0/0 from the environment: error %v", err)
	}
	if _, _, err := resolveEnv(t, `DASH_AUTH_PROXY_TRUSTED_PROXIES=["0.0.0.0/0"]`, "DASH_AUTH_PROXY_ALLOW_ANY_PEER=true"); err != nil {
		t.Errorf("0.0.0.0/0 with DASH_AUTH_PROXY_ALLOW_ANY_PEER=true: %v", err)
	}
}

// Every bad entry is reported, each naming itself, so one edit fixes the list.
func TestTrustedProxiesParseErrorsNameTheEntry(t *testing.T) {
	bad := []string{"10.0.0.0/33", "not-an-ip", "fe80::1%eth0", "10.0.0.1/", "300.1.1.1", ""}
	ps := authProblems(func(a *AuthConfig) { a.Proxy.TrustedProxies = append([]string{"127.0.0.1/32"}, bad...) })
	if len(ps) != len(bad) {
		t.Fatalf("%d problems for %d bad entries: %+v", len(ps), len(bad), ps)
	}
	for i, e := range bad {
		if !hasProblem(ps[i:i+1], "proxy.trustedProxies", `"`+e+`"`) {
			t.Errorf("entry %q: problem %+v does not name it at proxy.trustedProxies", e, ps[i])
		}
	}
	// The good entry beside them is not blamed, and nothing but the entry is echoed.
	for _, p := range ps {
		if strings.Contains(p.Msg, "127.0.0.1") {
			t.Errorf("problem %+v blames the valid entry", p)
		}
	}

	_, _, err := resolveEnv(t, `DASH_AUTH_PROXY_TRUSTED_PROXIES=["10.0.0.0/8","proxy.example.com"]`)
	if err == nil {
		t.Fatal("a host name was accepted as a trusted proxy")
	}
	for _, w := range []string{"auth.proxy.trustedProxies", "(env DASH_AUTH_PROXY_TRUSTED_PROXIES)", `"proxy.example.com"`} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
}

// The header that names the user must not be one dash itself reads for something else, and every
// name is an HTTP token; the same holds for the groups header.
func TestUserHeadersRefuseReservedNames(t *testing.T) {
	for _, h := range []string{"Authorization", "authorization", "Cookie", "COOKIE", "Host", "X-Dash-Csrf", "x-dash-csrf", "bad name", "X-User:", ""} {
		ps := authProblems(func(a *AuthConfig) { a.Proxy.UserHeaders = []string{"X-Forwarded-User", h} })
		if !hasProblem(ps, "proxy.userHeaders") || len(ps) != 1 {
			t.Errorf("user header %q: problems %+v", h, ps)
		}
		ps = authProblems(func(a *AuthConfig) { a.Proxy.GroupsHeader = h })
		if h == "" {
			if len(ps) != 0 {
				t.Errorf("an empty groups header is off, got %+v", ps)
			}
			continue
		}
		if !hasProblem(ps, "proxy.groupsHeader") || len(ps) != 1 {
			t.Errorf("groups header %q: problems %+v", h, ps)
		}
	}
	for name, list := range map[string][]string{"nil": nil, "empty": {}, "blank": {"  "}} {
		ps := authProblems(func(a *AuthConfig) { a.Proxy.UserHeaders = list })
		if !hasProblem(ps, "proxy.userHeaders") {
			t.Errorf("%s user header list: problems %+v", name, ps)
		}
	}
	// Surrounding space is not part of a name.
	if ps := authProblems(func(a *AuthConfig) {
		a.Proxy.UserHeaders = []string{" X-Email "}
		a.Proxy.GroupsHeader = " X-Forwarded-Groups "
	}); len(ps) != 0 {
		t.Errorf("padded names: problems %+v", ps)
	}

	_, _, err := resolveEnv(t, `DASH_AUTH_PROXY_USER_HEADERS=["Authorization"]`)
	if err == nil {
		t.Fatal("Authorization was accepted as a user header")
	}
	for _, w := range []string{"auth.proxy.userHeaders", "(env DASH_AUTH_PROXY_USER_HEADERS)", `"Authorization"`} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
}

// The admin lists are exact names: surrounding space and tab are pasted padding, but a Unicode
// space is part of the name and stays, as it does in a header value (auth.TrimIdentity).
func TestAdminListsKeepUnicodeSpace(t *testing.T) {
	a := AuthConfig{Proxy: AuthProxyConfig{
		AdminUsers:  []string{" alice\t", "\u00a0bob", "carol\u3000"},
		AdminGroups: []string{"\u00a0ops", " dev "},
	}}
	a.normalize()
	if want := []string{"alice", "\u00a0bob", "carol\u3000"}; !slices.Equal(a.Proxy.AdminUsers, want) {
		t.Errorf("adminUsers = %q, want %q", a.Proxy.AdminUsers, want)
	}
	if want := []string{"\u00a0ops", "dev"}; !slices.Equal(a.Proxy.AdminGroups, want) {
		t.Errorf("adminGroups = %q, want %q", a.Proxy.AdminGroups, want)
	}
}

// Groups are only read from a configured header, so adminGroups without one could never match:
// refused, with the setting to add named.
func TestAdminGroupsRequireAGroupsHeader(t *testing.T) {
	ps := authProblems(func(a *AuthConfig) { a.Proxy.AdminGroups = []string{"ops"} })
	if !hasProblem(ps, "proxy.adminGroups", "auth.proxy.groupsHeader") || len(ps) != 1 {
		t.Errorf("adminGroups without a groups header: problems %+v", ps)
	}
	ps = authProblems(func(a *AuthConfig) { a.Proxy.AdminGroups = []string{"ops"}; a.Proxy.GroupsHeader = "   " })
	if !hasProblem(ps, "proxy.adminGroups", "auth.proxy.groupsHeader") {
		t.Errorf("adminGroups beside a blank groups header: problems %+v", ps)
	}
	for name, set := range map[string]func(*AuthConfig){
		"header set": func(a *AuthConfig) {
			a.Proxy.AdminGroups = []string{"ops"}
			a.Proxy.GroupsHeader = "X-Forwarded-Groups"
		},
		"no groups, no header": func(a *AuthConfig) { a.Proxy.AdminGroups = nil },
		"header, no groups":    func(a *AuthConfig) { a.Proxy.GroupsHeader = "X-Forwarded-Groups" },
	} {
		if ps := authProblems(set); len(ps) != 0 {
			t.Errorf("%s: problems %+v", name, ps)
		}
	}

	_, _, err := resolveEnv(t, `DASH_AUTH_PROXY_ADMIN_GROUPS=["ops"]`)
	if err == nil {
		t.Fatal("DASH_AUTH_PROXY_ADMIN_GROUPS without a groups header was accepted")
	}
	for _, w := range []string{"auth.proxy.adminGroups", "(env DASH_AUTH_PROXY_ADMIN_GROUPS)", "auth.proxy.groupsHeader"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not contain %q", err, w)
		}
	}
	if _, _, err := resolveEnv(t, `DASH_AUTH_PROXY_ADMIN_GROUPS=["ops"]`, "DASH_AUTH_PROXY_GROUPS_HEADER=X-Forwarded-Groups"); err != nil {
		t.Errorf("adminGroups with a groups header: %v", err)
	}
}

// Entries are trimmed and must name something; "*" is the everyone-is-admin switch of adminUsers
// and means nothing as a group.
func TestAdminListEntriesAreValidated(t *testing.T) {
	withHeader := func(f func(*AuthConfig)) func(*AuthConfig) {
		return func(a *AuthConfig) { a.Proxy.GroupsHeader = "X-Forwarded-Groups"; f(a) }
	}
	users := func(l ...string) func(*AuthConfig) {
		return withHeader(func(a *AuthConfig) { a.Proxy.AdminUsers = l })
	}
	groups := func(l ...string) func(*AuthConfig) {
		return withHeader(func(a *AuthConfig) { a.Proxy.AdminGroups = l })
	}
	for name, c := range map[string]struct {
		set  func(*AuthConfig)
		path string // "" = valid
		msg  string
	}{
		"users":                    {users("TechDufus", "alice"), "", ""},
		"users padded":             {users("  TechDufus "), "", ""},
		"wildcard user":            {users("*"), "", ""},
		"empty user":               {users("alice", ""), "proxy.adminUsers", "entry 2 is empty"},
		"blank user":               {users("   "), "proxy.adminUsers", "entry 1 is empty"},
		"groups":                   {groups("ops", " sre "), "", ""},
		"empty group":              {groups(""), "proxy.adminGroups", "entry 1 is empty"},
		"blank group":              {groups("ops", "\t"), "proxy.adminGroups", "entry 2 is empty"},
		"wildcard group":           {groups("ops", "*"), "proxy.adminGroups", "only valid in adminUsers"},
		"padded wildcard group":    {groups(" * "), "proxy.adminGroups", "only valid in adminUsers"},
		"star inside a group name": {groups("ops*"), "", ""},
	} {
		ps := authProblems(c.set)
		if c.path == "" {
			if len(ps) != 0 {
				t.Errorf("%s: problems %+v", name, ps)
			}
			continue
		}
		if !hasProblem(ps, c.path, c.msg) || len(ps) != 1 {
			t.Errorf("%s: problems %+v, want one at %s containing %q", name, ps, c.path, c.msg)
		}
	}

	_, _, err := resolveEnv(t, `DASH_AUTH_PROXY_ADMIN_USERS=["alice",""]`)
	if err == nil || !strings.Contains(err.Error(), "auth.proxy.adminUsers") || !strings.Contains(err.Error(), "(env DASH_AUTH_PROXY_ADMIN_USERS)") {
		t.Errorf("an empty adminUsers entry from the environment: error %v", err)
	}
}

// An admin entry that no header could ever carry is a load error, by list and position: the user
// header is cut at 128 bytes of clean UTF-8 and a group at 256 (and the groups header is split at
// commas). v0.11.1 only warned (a patch release adds no load error); v0.12.0 refuses.
func TestAdminEntriesThatCanNeverMatchAreRefused(t *testing.T) {
	type at struct {
		path  string
		entry int
	}
	users := func(l ...string) func(*AuthConfig) { return func(a *AuthConfig) { a.Proxy.AdminUsers = l } }
	groups := func(l ...string) func(*AuthConfig) {
		return func(a *AuthConfig) { a.Proxy.GroupsHeader, a.Proxy.AdminGroups = "X-Forwarded-Groups", l }
	}
	const U, G = "proxy.adminUsers", "proxy.adminGroups"
	for name, c := range map[string]struct {
		set  func(*AuthConfig)
		want []at // nil = every entry can match
	}{
		"user of 128 bytes":           {users(strings.Repeat("a", 128)), nil},
		"user of 129 bytes":           {users("ok", strings.Repeat("a", 129)), []at{{U, 2}}},
		"user of 64 two-byte runes":   {users(strings.Repeat("é", 64)), nil},
		"user of 65 two-byte runes":   {users(strings.Repeat("é", 65)), []at{{U, 1}}},
		"user with a tab inside":      {users("al\tice"), []at{{U, 1}}},
		"user with a newline":         {users("alice\nbob"), []at{{U, 1}}},
		"user with DEL":               {users("al\x7fice"), []at{{U, 1}}},
		"user with a C1 control":      {users("al\u0085ice"), []at{{U, 1}}},
		"user that is not UTF-8":      {users("al\xffice"), []at{{U, 1}}},
		"user with a comma":           {users("smith, john"), nil},
		"padding is trimmed first":    {users(" \t" + strings.Repeat("a", 128) + "\t "), nil},
		"wildcard user":               {users("*"), nil},
		"group of 256 bytes":          {groups(strings.Repeat("g", 256)), nil},
		"group of 257 bytes":          {groups("ops", strings.Repeat("g", 257)), []at{{G, 2}}},
		"group with a comma":          {groups("ops", "a,b"), []at{{G, 2}}},
		"group with a control":        {groups("o\x00ps"), []at{{G, 1}}},
		"group that is not UTF-8":     {groups("o\xc3ps"), []at{{G, 1}}},
		"group with a C1 control":     {groups("o\u009fps"), []at{{G, 1}}},
		"group with a no-break space": {groups("\u00a0ops"), nil},
	} {
		ps := authProblems(c.set)
		if len(ps) != len(c.want) {
			t.Errorf("%s: problems %+v, want %d", name, ps, len(c.want))
			continue
		}
		for i, w := range c.want {
			if ps[i].Path != w.path || !strings.Contains(ps[i].Msg, fmt.Sprintf("entry %d can never match", w.entry)) {
				t.Errorf("%s: problem %+v, want entry %d of %s", name, ps[i], w.entry, w.path)
			}
		}
	}
}

// The refusal reaches the load (and `dash config validate`) naming the setting, the variable and
// the position, and nothing of the value.
func TestUnmatchableAdminEntriesFailTheLoad(t *testing.T) {
	_, _, err := resolveEnv(t, `DASH_AUTH_PROXY_ADMIN_USERS=["alice","hunter2\u0001-secret"]`,
		"DASH_AUTH_PROXY_GROUPS_HEADER=X-Forwarded-Groups", `DASH_AUTH_PROXY_ADMIN_GROUPS=["ops","a,b-secret"]`)
	if err == nil {
		t.Fatal("entries that can never match loaded")
	}
	for _, want := range []string{"auth.proxy.adminUsers (env DASH_AUTH_PROXY_ADMIN_USERS): entry 2 can never match", "auth.proxy.adminGroups (env DASH_AUTH_PROXY_ADMIN_GROUPS): entry 2 can never match"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "b-secret") {
		t.Errorf("the error carries a value: %v", err)
	}
	if _, log, err := resolveEnv(t, `DASH_AUTH_PROXY_ADMIN_USERS=["alice"]`); err != nil || strings.Contains(log, "never match") {
		t.Errorf("a valid list warned or failed: %v\n%s", err, log)
	}
}

// resolveLive resolves outside dev and demo, where the session-mode rules apply, with a public URL.
func resolveLive(t *testing.T, env ...string) (*Resolved, string, error) {
	t.Helper()
	log, buf := logBuf()
	res, err := Resolve(Sources{Environ: append([]string{"DASH_SERVER_PUBLIC_URL=https://dash.example.com"}, env...), Log: log})
	return res, buf.String(), err
}

// The local mode's keys: the account is named admin and the anonymous viewer is off unless set.
func TestAnonymousViewerIsOffByDefault(t *testing.T) {
	res, _, err := resolveLive(t, "DASH_AUTH_MODE=local")
	if err != nil {
		t.Fatal(err)
	}
	if l := res.Tree.Auth.Local; l.AnonymousViewer || l.AdminUser != "admin" {
		t.Errorf("auth.local = %+v, want adminUser admin and no anonymous viewer", l)
	}
	if _, _, err := resolveLive(t, "DASH_AUTH_MODE=local", "DASH_AUTH_LOCAL_ANONYMOUS_VIEWER=true"); err != nil {
		t.Errorf("anonymous viewer in local mode: %v", err)
	}
	// Outside the local mode it would be silently meaningless: refused.
	_, _, err = resolveLive(t, "DASH_AUTH_MODE=proxy", "DASH_AUTH_LOCAL_ANONYMOUS_VIEWER=true")
	if err == nil || !strings.Contains(err.Error(), "auth.local.anonymousViewer") || !strings.Contains(err.Error(), "only available in auth.mode=local") {
		t.Errorf("anonymous viewer in proxy mode: %v, want refused", err)
	}
	for _, bad := range []string{"", " ", "a\x01b", strings.Repeat("x", 129)} {
		if !hasProblem(authProblems(func(a *AuthConfig) { a.Local.AdminUser = bad }), "local.adminUser") {
			t.Errorf("adminUser %q accepted", bad)
		}
	}
}

// Proxy settings beside a session mode are never read: dash says so at load, naming the settings
// and never their values; outside a session mode, and in dev or demo, it says nothing.
func TestProxyKeysWithoutProxyModeWarn(t *testing.T) {
	env := []string{`DASH_AUTH_PROXY_USER_HEADERS=["X-Secret-Header"]`, `DASH_AUTH_PROXY_TRUSTED_PROXIES=["10.9.8.0/24"]`}
	_, log, err := resolveLive(t, append([]string{"DASH_AUTH_MODE=local"}, env...)...)
	if err != nil {
		t.Fatalf("the configuration must load: %v", err)
	}
	if !strings.Contains(log, "WARN") || !strings.Contains(log, "auth.proxy settings are ignored") ||
		!strings.Contains(log, "auth.proxy.userHeaders") || !strings.Contains(log, "auth.proxy.trustedProxies") || !strings.Contains(log, "mode=local") {
		t.Errorf("log = %q, want one WARN naming both settings", log)
	}
	if strings.Contains(log, "X-Secret-Header") || strings.Contains(log, "10.9.8.0") {
		t.Errorf("the warning carries a value:\n%s", log)
	}
	if _, log, _ := resolveLive(t, append([]string{"DASH_AUTH_MODE=proxy"}, env...)...); strings.Contains(log, "are ignored") {
		t.Errorf("proxy mode warned: %s", log)
	}
	if _, log, _ := resolveEnv(t, append([]string{"DASH_AUTH_MODE=local"}, env...)...); strings.Contains(log, "are ignored") {
		t.Errorf("demo mode warned: %s", log)
	}
	if _, log, _ := resolveLive(t, "DASH_AUTH_MODE=local"); strings.Contains(log, "are ignored") {
		t.Errorf("defaults alone warned: %s", log)
	}
}

// The proxy admin lists beside a session mode refuse to load: an install that relied on the
// implicit proxy mode would otherwise lose its administrators without a word.
func TestProxyAdminListsWithoutProxyModeRefuse(t *testing.T) {
	for name, env := range map[string][]string{
		"auth.proxy.adminUsers":  {`DASH_AUTH_PROXY_ADMIN_USERS=["owner-name"]`},
		"auth.proxy.adminGroups": {"DASH_AUTH_PROXY_GROUPS_HEADER=X-Forwarded-Groups", `DASH_AUTH_PROXY_ADMIN_GROUPS=["owner-group"]`},
	} {
		_, _, err := resolveLive(t, append([]string{"DASH_AUTH_MODE=local"}, env...)...)
		if err == nil {
			t.Errorf("%s with auth.mode=local loaded", name)
			continue
		}
		msg := err.Error()
		for _, want := range []string{name, "(env DASH_AUTH_PROXY_ADMIN_", "DASH_AUTH_MODE=proxy"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s: error %q lacks %q", name, msg, want)
			}
		}
		if strings.Contains(msg, "owner-") {
			t.Errorf("%s: error carries the value: %q", name, msg)
		}
		// The mode was set explicitly: the message does not call it the default.
		if strings.Contains(msg, "the default since") {
			t.Errorf("%s: an explicit auth.mode is called the default: %q", name, msg)
		}
		if _, _, err := resolveLive(t, append([]string{"DASH_AUTH_MODE=proxy"}, env...)...); err != nil {
			t.Errorf("%s with auth.mode=proxy: %v", name, err)
		}
		if _, _, err := resolveEnv(t, append([]string{"DASH_AUTH_MODE=local"}, env...)...); err != nil {
			t.Errorf("%s in demo mode (the mode is ignored): %v", name, err)
		}
	}
}

// DASH_ADMIN_PASSWORD_HASH is process environment: read directly or from its _FILE, never both,
// registered for scrubbing, and never an unknown-variable warning.
func TestAdminPasswordHashEnv(t *testing.T) {
	const hash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g"
	reg := secret.NewRegistry()
	if got, err := AdminPasswordHash([]string{"DASH_ADMIN_PASSWORD_HASH= " + hash + " "}, reg); err != nil || got != hash {
		t.Errorf("direct = %q %v", got, err)
	}
	if out := reg.Scrub("x " + hash + " y"); strings.Contains(out, hash) {
		t.Errorf("the hash is not registered: %q", out)
	}
	file := filepath.Join(t.TempDir(), "hash")
	if err := os.WriteFile(file, []byte(hash+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := AdminPasswordHash([]string{"DASH_ADMIN_PASSWORD_HASH_FILE=" + file}, nil); err != nil || got != hash {
		t.Errorf("file = %q %v", got, err)
	}
	if _, err := AdminPasswordHash([]string{"DASH_ADMIN_PASSWORD_HASH=" + hash, "DASH_ADMIN_PASSWORD_HASH_FILE=" + file}, nil); err == nil || strings.Contains(err.Error(), hash) {
		t.Errorf("both set: %v, want an error without the value", err)
	}
	if got, err := AdminPasswordHash(nil, nil); err != nil || got != "" {
		t.Errorf("unset = %q %v", got, err)
	}
	if _, log, err := resolveEnv(t, "DASH_ADMIN_PASSWORD_HASH="+hash); err != nil || strings.Contains(log, "unknown") {
		t.Errorf("the variable warned as unknown or failed: %v\n%s", err, log)
	}
}

// oidcEnv is a complete, valid oidc configuration in the environment, plus more.
func oidcEnv(more ...string) []string {
	return append([]string{
		"DASH_AUTH_MODE=oidc", "DASH_AUTH_OIDC_ISSUER=https://login.example.com/realms/home",
		"DASH_AUTH_OIDC_CLIENT_ID=dash", "DASH_AUTH_OIDC_CLIENT_SECRET=client-secret-value",
	}, more...)
}

// oidcProblems is what Validate reports for an oidc-mode section changed by mutate.
func oidcProblems(mutate func(*AuthOIDCConfig)) []pluginconf.FieldError {
	return authProblems(func(a *AuthConfig) {
		a.Mode = AuthModeOIDC
		a.OIDC.Issuer, a.OIDC.ClientID = "https://login.example.com", "dash"
		a.OIDC.ClientSecret = secret.New("client-secret-value", "")
		a.OIDC.AllowedUsers = []string{"alice"}
		mutate(&a.OIDC)
	})
}

// oidc fails closed (G4-R2-B2): without an allow-list nobody is admitted on purpose, so dash does
// not start; an explicit "*" in allowedUsers is the way to admit every account.
func TestOIDCRequiresAnAllowListOrExplicitWildcard(t *testing.T) {
	_, _, err := resolveLive(t, oidcEnv()...)
	if err == nil || !strings.Contains(err.Error(), "auth.oidc.allowedUsers") || !strings.Contains(err.Error(), "allowedGroups") {
		t.Fatalf("no allow-list: %v, want a refusal naming auth.oidc.allowedUsers", err)
	}
	for name, env := range map[string][]string{
		"users":    {`DASH_AUTH_OIDC_ALLOWED_USERS=["alice"]`},
		"groups":   {`DASH_AUTH_OIDC_ALLOWED_GROUPS=["family"]`},
		"wildcard": {`DASH_AUTH_OIDC_ALLOWED_USERS=["*"]`},
	} {
		if _, _, err := resolveLive(t, oidcEnv(env...)...); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The other modes do not need one.
	if _, _, err := resolveLive(t, "DASH_AUTH_MODE=local"); err != nil {
		t.Errorf("local mode: %v", err)
	}
	// The provider and the client are required in the oidc mode, and named.
	ps := authProblems(func(a *AuthConfig) { a.Mode = AuthModeOIDC; a.OIDC.AllowedUsers = []string{"*"} })
	for _, path := range []string{"oidc.issuer", "oidc.clientID", "oidc.clientSecret"} {
		if !hasProblem(ps, path, "required in auth.mode=oidc") {
			t.Errorf("%s not required: %+v", path, ps)
		}
	}
}

// The issuer is https, or http only on a loopback host; the public URL the provider redirects to
// is https the same way.
func TestOIDCIssuerMustBeHTTPSOrLoopback(t *testing.T) {
	for issuer, ok := range map[string]bool{
		"https://login.example.com":             true,
		"https://login.example.com/realms/home": true,
		"http://127.0.0.1:5556/dex":             true,
		"http://localhost:8080":                 true,
		"http://[::1]:8080":                     true,
		"http://login.example.com":              false,
		"ftp://login.example.com":               false,
		"https://":                              false,
		"login.example.com":                     false,
		"https://user:pw@login.example.com":     false,
		"https://login.example.com/?x=1":        false,
	} {
		got := !hasProblem(oidcProblems(func(o *AuthOIDCConfig) { o.Issuer = issuer }), "oidc.issuer")
		if got != ok {
			t.Errorf("issuer %q accepted = %v, want %v", issuer, got, ok)
		}
	}
	_, _, err := resolveWith(t, Sources{Environ: append(oidcEnv(`DASH_AUTH_OIDC_ALLOWED_USERS=["alice"]`), "DASH_SERVER_PUBLIC_URL=http://dash.example.com")})
	if err == nil || !strings.Contains(err.Error(), "server.publicURL") || !strings.Contains(err.Error(), "https in auth.mode=oidc") {
		t.Errorf("http public URL in oidc mode: %v, want refused", err)
	}
	if _, _, err := resolveWith(t, Sources{Environ: append(oidcEnv(`DASH_AUTH_OIDC_ALLOWED_USERS=["alice"]`), "DASH_SERVER_PUBLIC_URL=http://127.0.0.1:8080")}); err != nil {
		t.Errorf("loopback http public URL: %v", err)
	}
}

// listRow is one entry of an oidc list and whether Validate accepts it.
type listRow struct {
	entry string
	ok    bool
}

// checkOIDCList runs rows through the list set by set, at path: an accepted entry raises no
// problem there, a refused one raises one naming entry 2, and no message carries the entry.
func checkOIDCList(t *testing.T, path string, set func(*AuthOIDCConfig, []string), rows []listRow) {
	t.Helper()
	for _, r := range rows {
		ps := oidcProblems(func(o *AuthOIDCConfig) { set(o, []string{"fine", r.entry}) })
		bad := hasProblem(ps, path, "entry 2")
		if bad == r.ok {
			t.Errorf("%s entry %q: refused = %v, want %v (%+v)", path, r.entry, bad, !r.ok, ps)
		}
		for _, p := range ps {
			if strings.TrimSpace(r.entry) != "" && strings.Contains(p.Msg, strings.TrimSpace(r.entry)) && r.entry != "*" {
				t.Errorf("%s: the message %q carries the entry", path, p.Msg)
			}
		}
	}
}

// The user lists accept what the user claim can be (a valid user name) and "*"; commas are
// fine in a name.
var oidcUserRows = []listRow{
	{"alice", true}, {"alice@example.com", true}, {"Smith, Jane", true}, {"*", true}, {"  padded  ", true},
	{"", false}, {"  ", false}, {"bad\x01name", false}, {"x\xffy", false}, {strings.Repeat("u", 129), false},
}

// The group and role lists accept what claimValues keeps (a valid group name): never a comma,
// never "*".
var oidcValueRows = []listRow{
	{"family", true}, {"/org/team", true}, {strings.Repeat("g", 256), true},
	{"", false}, {"*", false}, {"a,b", false}, {"bad\x01group", false}, {strings.Repeat("g", 257), false},
}

func TestOIDCAdminUsersEntries(t *testing.T) {
	checkOIDCList(t, "oidc.adminUsers", func(o *AuthOIDCConfig, l []string) { o.AdminUsers = l }, oidcUserRows)
}

func TestOIDCAllowedUsersEntries(t *testing.T) {
	checkOIDCList(t, "oidc.allowedUsers", func(o *AuthOIDCConfig, l []string) { o.AllowedUsers = l }, oidcUserRows)
}

func TestOIDCAdminGroupsEntries(t *testing.T) {
	checkOIDCList(t, "oidc.adminGroups", func(o *AuthOIDCConfig, l []string) { o.AdminGroups = l }, oidcValueRows)
}

func TestOIDCAllowedGroupsEntries(t *testing.T) {
	checkOIDCList(t, "oidc.allowedGroups", func(o *AuthOIDCConfig, l []string) { o.AllowedGroups = l }, oidcValueRows)
}

func TestOIDCAdminRolesEntries(t *testing.T) {
	checkOIDCList(t, "oidc.adminRoles", func(o *AuthOIDCConfig, l []string) { o.AdminRoles = l }, oidcValueRows)
}

// The group lists need the groups claim, and the list rules hold in every mode: they are load
// errors from the start, through the real resolver too.
func TestOIDCListRulesAtLoad(t *testing.T) {
	for _, path := range []string{"oidc.adminGroups", "oidc.allowedGroups"} {
		ps := oidcProblems(func(o *AuthOIDCConfig) {
			o.GroupsClaim = ""
			o.AdminGroups, o.AllowedGroups = []string{"ops"}, []string{"family"}
		})
		if !hasProblem(ps, path, "needs auth.oidc.groupsClaim") {
			t.Errorf("%s without groupsClaim accepted: %+v", path, ps)
		}
	}
	_, _, err := resolveLive(t, "DASH_AUTH_MODE=proxy", `DASH_AUTH_OIDC_ADMIN_GROUPS=["a,b-hidden"]`)
	if err == nil || !strings.Contains(err.Error(), "auth.oidc.adminGroups") || strings.Contains(err.Error(), "b-hidden") {
		t.Errorf("a never-matching entry outside the oidc mode: %v, want a load error without the value", err)
	}
}

// The default flip to local keeps every pinned deployment loading: v0.8 to v0.10 set no auth key
// (they load in the local mode, whose missing account is a run-time refusal, not a load error) and
// v0.11 sets proxy. Nothing warns about the auth section.
func TestOldDeploymentPinsStillLoadAfterTheDefaultFlip(t *testing.T) {
	for file, mode := range map[string]string{
		deploymentEnvFileV08: AuthModeLocal, deploymentEnvFileV09: AuthModeLocal,
		deploymentEnvFileV010: AuthModeLocal, deploymentEnvFileV011: AuthModeProxy,
	} {
		res, log, err := resolveWith(t, Sources{Environ: deploymentEnv(t, file)})
		if err != nil {
			t.Errorf("%s does not load: %v", file, err)
			continue
		}
		if res.Tree.Auth.Mode != mode || strings.Contains(log, "auth.") {
			t.Errorf("%s: mode %q, log %q; want %s and no auth warning", file, res.Tree.Auth.Mode, log, mode)
		}
	}
}

// The homelab environment without its DASH_AUTH_MODE line no longer loads: its proxy admin list
// would be ignored in the new default mode, so the load names the line to add.
func TestHomelabEnvWithoutAuthModeIsRefusedAtLoad(t *testing.T) {
	var env []string
	for _, kv := range deploymentEnv(t, deploymentEnvFileV011) {
		if !strings.HasPrefix(kv, "DASH_AUTH_MODE=") {
			env = append(env, kv)
		}
	}
	_, _, err := resolveWith(t, Sources{Environ: env})
	if err == nil || !strings.Contains(err.Error(), "auth.proxy.adminUsers (env DASH_AUTH_PROXY_ADMIN_USERS)") || !strings.Contains(err.Error(), "set DASH_AUTH_MODE=proxy") {
		t.Errorf("the homelab env without its mode: %v, want the refusal naming DASH_AUTH_MODE=proxy", err)
	}
	// The operator never set auth.mode: the message says local is the new default.
	if err == nil || !strings.Contains(err.Error(), "auth.mode is local (the default since v0.12.0)") {
		t.Errorf("the homelab env without its mode: %v, want it to say local is the default", err)
	}
}

// An inline launcher icon outside the allowed forms fails the load (v0.11.1 only turned the
// launcher row to error), naming the entry and not the icon; the allowed forms load.
func TestLauncherIconOutsideTheAllowedFormsFailsTheLoad(t *testing.T) {
	const base = "DASH_UI_ICON_BASE_URL=https://cdn.example.com/png"
	for icon, ok := range map[string]bool{
		"proxmox":                            true,
		"/icons/x.svg":                       true,
		"https://cdn.example.com/png/x.png":  true,
		"javascript:alert(1)":                false,
		"data:image/png;base64,AAAA":         false,
		"https://evil.example.com/png/x.png": false,
		"//evil.example.com/x.png":           false,
	} {
		links := fmt.Sprintf(`DASH_PLUGINS_LAUNCHER_LINKS=[{"name":"a","href":"https://a.example.com"},{"name":"b","href":"https://b.example.com","icon":%q}]`, icon)
		_, _, err := resolveLive(t, base, links)
		switch {
		case ok && err != nil:
			t.Errorf("icon %q: %v, want it to load", icon, err)
		case !ok && (err == nil || !strings.Contains(err.Error(), "plugins.launcher.links") || !strings.Contains(err.Error(), "entry 2")):
			t.Errorf("icon %q: %v, want a load error naming entry 2", icon, err)
		case !ok && strings.Contains(err.Error(), icon):
			t.Errorf("icon %q: the error carries the icon: %v", icon, err)
		}
	}
}
