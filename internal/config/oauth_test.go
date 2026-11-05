package config

import (
	"strings"
	"testing"
)

// server.directURL is an origin other than the public URL's, https unless loopback, and never
// with auth.mode=none; a refusal names the key, never the value.
func TestDirectURL(t *testing.T) {
	base := map[string]string{"DASH_SERVER_PUBLIC_URL": "https://dash.example.com", "DASH_AUTH_MODE": "proxy"}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	c := mustLoad(t, with("DASH_SERVER_DIRECT_URL", " https://Direct.example.com/ "))
	if c.DirectURL != "https://Direct.example.com" {
		t.Errorf("DirectURL = %q", c.DirectURL)
	}
	if !c.HostAllowed("direct.example.com:443") || !c.HostAllowed("dash.example.com") || c.HostAllowed("other.example.com") {
		t.Error("the Host allow-list does not take the direct host next to the public one")
	}
	if Defaults().HostAllowed("direct.example.com") {
		t.Error("an unset directURL allows a host")
	}
	mustLoad(t, with("DASH_SERVER_DIRECT_URL", "http://127.0.0.1:9000"))
	for _, tc := range []struct{ name, value, want string }{
		{"path", "https://direct.example.com/api", "origin"},
		{"query", "https://direct.example.com?x=1", "origin"},
		{"fragment", "https://direct.example.com#x", "origin"},
		{"user info", "https://pw@direct.example.com", "user information"},
		{"plain http", "http://direct.example.com", "https"},
		{"the public origin", "https://dash.example.com", "another origin"},
		{"not a URL", "direct.example.com", "http(s) URL"},
	} {
		_, err := load(with("DASH_SERVER_DIRECT_URL", tc.value))
		if err == nil || !strings.Contains(err.Error(), "server.directURL") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want a server.directURL error about %q", tc.name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "direct.example.com") {
			t.Errorf("%s: the error repeats the value: %v", tc.name, err)
		}
	}
	none := with("DASH_SERVER_DIRECT_URL", "http://127.0.0.1:9000", "DASH_AUTH_MODE", "none", "DASH_SERVER_LISTEN", "127.0.0.1:8080")
	if _, err := load(none); err == nil || !strings.Contains(err.Error(), "server.directURL") || !strings.Contains(err.Error(), "auth.mode=none") {
		t.Errorf("none mode: %v, want refused", err)
	}
}

// auth.oauth is off by default; its clients follow the redirect rules of client ID metadata
// documents, and errors name the entry by position.
func TestOAuthConfig(t *testing.T) {
	res, _, err := resolveWith(t, Sources{Environ: environ(map[string]string{
		"DASH_SERVER_PUBLIC_URL":  "https://dash.example.com",
		"DASH_AUTH_OAUTH_ENABLED": "true", "DASH_AUTH_OAUTH_CIMD_ALLOW_PRIVATE": "true",
		"DASH_AUTH_OAUTH_CLIENTS": `[{"clientID":"inspector","name":"Inspector","redirectURIs":["http://127.0.0.1/cb","https://app.example.com/cb","com.example.app:/oauth"],"audiences":["mcp"]}]`,
	})})
	if err != nil {
		t.Fatal(err)
	}
	o := res.Tree.Auth.OAuth
	if !o.Enabled || !o.CIMDAllowPrivate || len(o.Clients) != 1 || o.Clients[0].ClientID != "inspector" || len(o.Clients[0].RedirectURIs) != 3 {
		t.Errorf("auth.oauth = %+v", o)
	}
	if d := DefaultAuth().OAuth; d.Enabled || d.CIMDAllowPrivate || d.Clients != nil {
		t.Errorf("default auth.oauth = %+v, want off", d)
	}

	for _, tc := range []struct{ name, clients, want string }{
		{"no client ID", `[{"name":"A","redirectURIs":["https://a.example.com/cb"],"audiences":["mcp"]}]`, "entry 1 needs a clientID"},
		{"the built-in app", `[{"clientID":"dash-android","name":"A","redirectURIs":["https://a.example.com/cb"],"audiences":["api"]}]`, "dash-android"},
		{"a URL client ID", `[{"clientID":"https://a.example.com/client.json","name":"A","redirectURIs":["https://a.example.com/cb"],"audiences":["mcp"]}]`, "client ID metadata document"},
		{"repeated", `[{"clientID":"a","name":"A","redirectURIs":["https://a.example.com/cb"],"audiences":["mcp"]},{"clientID":"a","name":"B","redirectURIs":["https://a.example.com/cb"],"audiences":["mcp"]}]`, "entry 2 repeats"},
		{"no name", `[{"clientID":"a","redirectURIs":["https://a.example.com/cb"],"audiences":["mcp"]}]`, "needs a name"},
		{"no redirect", `[{"clientID":"a","name":"A","audiences":["mcp"]}]`, "at least one redirect URI"},
		{"http redirect off loopback", `[{"clientID":"a","name":"A","redirectURIs":["http://a.example.com/cb"],"audiences":["mcp"]}]`, "redirect URI 1 is http on a host that is not loopback"},
		{"a fragment", `[{"clientID":"a","name":"A","redirectURIs":["https://a.example.com/cb#x"],"audiences":["mcp"]}]`, "fragment"},
		{"a scheme without a dot", `[{"clientID":"a","name":"A","redirectURIs":["myapp:/cb"],"audiences":["mcp"]}]`, "private-use scheme containing a dot"},
		{"javascript", `[{"clientID":"a","name":"A","redirectURIs":["javascript:alert(1)"],"audiences":["mcp"]}]`, "private-use scheme"},
		{"no audience", `[{"clientID":"a","name":"A","redirectURIs":["https://a.example.com/cb"]}]`, "at least one audience"},
		{"a wrong audience", `[{"clientID":"a","name":"A","redirectURIs":["https://a.example.com/cb"],"audiences":["admin"]}]`, "audience 1 is not api or mcp"},
	} {
		_, err := load(map[string]string{"DASH_SERVER_PUBLIC_URL": "https://dash.example.com", "DASH_AUTH_OAUTH_CLIENTS": tc.clients})
		if err == nil || !strings.Contains(err.Error(), "auth.oauth.clients") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "a.example.com") {
			t.Errorf("%s: the error repeats a value: %v", tc.name, err)
		}
	}
}

// With OAuth on, the public URL may not have a path: the issuer and the /.well-known documents
// are at the root of the origin. Without OAuth, a path stays as it was.
func TestOAuthRefusesAPublicURLPath(t *testing.T) {
	env := map[string]string{"DASH_SERVER_PUBLIC_URL": "https://dash.example.com/dash"}
	mustLoad(t, env)
	env["DASH_AUTH_OAUTH_ENABLED"] = "true"
	if _, err := load(env); err == nil || !strings.Contains(err.Error(), "server.publicURL") || !strings.Contains(err.Error(), "path") {
		t.Errorf("a public URL path with OAuth: %v, want refused", err)
	}
}

func TestRedirectURIProblem(t *testing.T) {
	for uri, ok := range map[string]bool{
		"https://app.example.com/cb":               true,
		"http://127.0.0.1/cb":                      true,
		"http://127.0.0.1:4711/cb":                 true,
		"http://[::1]:4711/cb":                     true,
		"http://localhost:4711/cb":                 true,
		"io.github.techdufus.dash:/oauth2redirect": true,
		"http://192.0.2.1/cb":                      false,
		"https:///cb":                              false,
		"https://u:p@app.example.com/cb":           false,
		"https://app.example.com/cb#f":             false,
		"data:text/html,x":                         false,
		"/cb":                                      false,
		"":                                         false,
		"https://app.example.com/c b":              false,
	} {
		if got := RedirectURIProblem(uri) == ""; got != ok {
			t.Errorf("%q: accepted %v, want %v (%s)", uri, got, ok, RedirectURIProblem(uri))
		}
	}
}
