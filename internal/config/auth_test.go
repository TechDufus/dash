package config

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/pluginconf"
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

// No env and no file: the proxy mode, the two user headers, groups off, the loopback believed and
// nobody an administrator. The expectation is written out, not derived from DefaultAuth.
func TestAuthDefaults(t *testing.T) {
	res, _, err := resolveEnv(t)
	if err != nil {
		t.Fatal(err)
	}
	a := res.Tree.Auth
	if a.Mode != "proxy" {
		t.Errorf("auth.mode = %q, want proxy", a.Mode)
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
		"DASH_AUTH_MODE":                  {"auth.mode", "string", "proxy", true, "proxy", "proxy"},
		"DASH_AUTH_PROXY_USER_HEADERS":    {"auth.proxy.userHeaders", "list of strings (JSON)", `["X-Forwarded-Preferred-Username","X-Forwarded-User"]`, true, `["X-Email"]`, []string{"X-Email"}},
		"DASH_AUTH_PROXY_GROUPS_HEADER":   {"auth.proxy.groupsHeader", "string", "", true, "X-Forwarded-Groups", "X-Forwarded-Groups"},
		"DASH_AUTH_PROXY_TRUSTED_PROXIES": {"auth.proxy.trustedProxies", "list of strings (JSON)", `["127.0.0.1/32","::1/128"]`, true, `["10.0.0.0/8"]`, []string{"10.0.0.0/8"}},
		"DASH_AUTH_PROXY_ALLOW_ANY_PEER":  {"auth.proxy.allowAnyPeer", "boolean", "", true, "true", true},
		"DASH_AUTH_PROXY_ADMIN_USERS":     {"auth.proxy.adminUsers", "list of strings (JSON)", "", false, `["alice"]`, []string{"alice"}},
		"DASH_AUTH_PROXY_ADMIN_GROUPS":    {"auth.proxy.adminGroups", "list of strings (JSON)", "", false, `["ops"]`, []string{"ops"}},
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
		if got.Secret {
			t.Errorf("%s is a secret", name)
		}
		if got.Description == "" {
			t.Errorf("%s has no description", name)
		}
		env := []string{name + "=" + want.set}
		if name == "DASH_AUTH_PROXY_ADMIN_GROUPS" {
			// adminGroups is only valid beside a groups header.
			env = append(env, "DASH_AUTH_PROXY_GROUPS_HEADER=X-Forwarded-Groups")
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
	for _, mode := range []string{"oidc", "Proxy", "none-of-them"} {
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

// An admin entry that no header could ever carry grants nothing, silently: the user header is cut
// at 128 bytes of clean UTF-8 and a group at 256 (and the groups header is split at commas), so an
// entry beyond that is refused at load, by position and never echoed.
func TestAdminEntriesThatCanNeverMatchAreRefused(t *testing.T) {
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
		"user of 128 bytes":           {users(strings.Repeat("a", 128)), "", ""},
		"user of 129 bytes":           {users("ok", strings.Repeat("a", 129)), "proxy.adminUsers", "entry 2 can never match"},
		"user of 64 two-byte runes":   {users(strings.Repeat("é", 64)), "", ""},
		"user of 65 two-byte runes":   {users(strings.Repeat("é", 65)), "proxy.adminUsers", "entry 1 can never match"},
		"user with a tab inside":      {users("al\tice"), "proxy.adminUsers", "entry 1 can never match"},
		"user with a newline":         {users("alice\nbob"), "proxy.adminUsers", "entry 1 can never match"},
		"user with DEL":               {users("al\x7fice"), "proxy.adminUsers", "entry 1 can never match"},
		"user with a C1 control":      {users("al\u0085ice"), "proxy.adminUsers", "entry 1 can never match"},
		"user that is not UTF-8":      {users("al\xffice"), "proxy.adminUsers", "entry 1 can never match"},
		"user with a comma":           {users("smith, john"), "", ""},
		"padding is trimmed first":    {users(" \t" + strings.Repeat("a", 128) + "\t "), "", ""},
		"wildcard user":               {users("*"), "", ""},
		"group of 256 bytes":          {groups(strings.Repeat("g", 256)), "", ""},
		"group of 257 bytes":          {groups("ops", strings.Repeat("g", 257)), "proxy.adminGroups", "entry 2 can never match"},
		"group with a comma":          {groups("ops", "a,b"), "proxy.adminGroups", "entry 2 can never match"},
		"group with a control":        {groups("o\x00ps"), "proxy.adminGroups", "entry 1 can never match"},
		"group that is not UTF-8":     {groups("o\xc3ps"), "proxy.adminGroups", "entry 1 can never match"},
		"group with a C1 control":     {groups("o\u009fps"), "proxy.adminGroups", "entry 1 can never match"},
		"group with a no-break space": {groups("\u00a0ops"), "", ""},
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

	// The message names the entry by position and never repeats it.
	secret := "hunter2-\x01-secret"
	ps := authProblems(func(a *AuthConfig) { a.Proxy.AdminUsers = []string{secret} })
	if len(ps) != 1 || strings.Contains(ps[0].Msg, "hunter2") {
		t.Errorf("an unusable admin user is echoed or unreported: %+v", ps)
	}

	// From the environment the problem names the variable too.
	_, _, err := resolveEnv(t, `DASH_AUTH_PROXY_ADMIN_USERS=["alice","bad\u0001name"]`)
	if err == nil || !strings.Contains(err.Error(), "auth.proxy.adminUsers") ||
		!strings.Contains(err.Error(), "(env DASH_AUTH_PROXY_ADMIN_USERS)") || !strings.Contains(err.Error(), "entry 2 can never match") {
		t.Errorf("an unusable adminUsers entry from the environment: error %v", err)
	}
}
