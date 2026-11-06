package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/model"
)

// mcpInit is a legacy initialize request: the simplest call every MCP server answers 200.
const mcpInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

type mcpEnv struct {
	s       *Server
	f       *tokenFixture
	on      bool
	mcpTok  string
	apiTok  string
	readTok string
}

func newMCPEnv(t *testing.T, mod ...func(*Deps)) *mcpEnv {
	t.Helper()
	e := &mcpEnv{on: true}
	f := newTokenFixture(t)
	e.f = f
	e.mcpTok, _ = f.pat(t, "bob", "mcp", []string{"read", "logs"})
	e.readTok, _ = f.pat(t, "bob", "mcp", []string{"read"})
	e.apiTok, _ = f.pat(t, "bob", "api", []string{"read", "logs"})
	e.s = newServer(func(d *Deps) {
		f.deps(d)
		d.MCPEnabled = func() bool { return e.on }
		d.Cfg.CORSOrigins = []string{"https://inspector.example.com"}
		for _, m := range mod {
			m(d)
		}
	})
	return e
}

// post sends an MCP request with the given headers; "" removes a header req.do sets.
func (e *mcpEnv) post(header map[string]string) *httptest.ResponseRecorder {
	h := map[string]string{
		"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
		"X-Dash-Csrf": "", "Origin": "",
	}
	for k, v := range header {
		h[k] = v
	}
	return req{method: http.MethodPost, path: "/mcp", body: mcpInit, header: h, raw: true}.do(e.s.Handler())
}

func withBearer(token string, more ...string) map[string]string {
	h := bearer(token)
	for i := 0; i+1 < len(more); i += 2 {
		h[more[i]] = more[i+1]
	}
	return h
}

// With MCP off, /mcp is a JSON 404 whatever the request carries, and so is its
// protected-resource metadata; switching it on takes effect at the next request.
func TestMCPDisabledIs404(t *testing.T) {
	e := newMCPEnv(t)
	e.on = false
	for name, h := range map[string]map[string]string{
		"a valid token": withBearer(e.mcpTok), "no token": nil, "a bad origin": withBearer(e.mcpTok, "Origin", "https://evil.example.com"),
	} {
		w := e.post(h)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// A listed origin's preflight too: no Access-Control-* header, nothing to suggest /mcp exists.
	pre := req{method: http.MethodOptions, path: "/mcp", raw: true, header: map[string]string{
		"Origin": "https://inspector.example.com", "Access-Control-Request-Method": "POST",
	}}.do(e.s.Handler())
	if pre.Code != http.StatusNotFound || pre.Header().Get("Access-Control-Allow-Origin") != "" || pre.Header().Get("Access-Control-Allow-Methods") != "" {
		t.Errorf("preflight: %d %v", pre.Code, pre.Header())
	}
	if w := (req{path: "/.well-known/oauth-protected-resource/mcp"}).do(e.s.Handler()); w.Code != http.StatusNotFound {
		t.Errorf("metadata: %d", w.Code)
	}
	e.on = true
	if w := e.post(withBearer(e.mcpTok)); w.Code != http.StatusOK {
		t.Errorf("after enabling: %d %s", w.Code, w.Body)
	}
}

// Only a bearer token of audience mcp reaches the server: an api token, no token (the identity
// proxy's user does not count) and a session cookie alongside are refused before it.
func TestMCPNeedsAnMCPAudienceToken(t *testing.T) {
	e := newMCPEnv(t)
	if w := e.post(withBearer(e.mcpTok)); w.Code != http.StatusOK {
		t.Fatalf("mcp token: %d %s", w.Code, w.Body)
	}
	for name, tc := range map[string]struct {
		h    map[string]string
		code int
	}{
		"api token":         {withBearer(e.apiTok), http.StatusUnauthorized},
		"no token":          {nil, http.StatusUnauthorized},
		"unknown token":     {withBearer("dash_pat_aaaaaaaaaa_" + strings.Repeat("b", 43)), http.StatusUnauthorized},
		"token plus cookie": {withBearer(e.mcpTok, "Cookie", "__Host-dash_session=x"), http.StatusBadRequest},
	} {
		w := e.post(tc.h)
		if w.Code != tc.code {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
		if tc.code == http.StatusUnauthorized && !strings.Contains(w.Header().Get("WWW-Authenticate"), "/.well-known/oauth-protected-resource/mcp") {
			t.Errorf("%s: challenge %q", name, w.Header().Get("WWW-Authenticate"))
		}
	}
	// And an mcp token is no credential at /api.
	if w := (req{path: "/api/v1/me", header: bearer(e.mcpTok)}).do(e.s.Handler()); w.Code != http.StatusUnauthorized {
		t.Errorf("mcp token at /api: %d", w.Code)
	}
}

// A present Origin must be the public URL's or a listed CORS origin (design N.1); no Origin is a
// program, which is fine.
func TestMCPOriginRule(t *testing.T) {
	e := newMCPEnv(t)
	for origin, want := range map[string]int{
		"":                              http.StatusOK,
		testOrigin:                      http.StatusOK,
		"https://inspector.example.com": http.StatusOK,
		"https://evil.example.com":      http.StatusForbidden,
		"null":                          http.StatusForbidden,
	} {
		w := e.post(withBearer(e.mcpTok, "Origin", origin))
		if w.Code != want {
			t.Errorf("Origin %q: %d %s", origin, w.Code, w.Body)
		}
		if want == http.StatusForbidden && decodeErrCode(t, w) != model.ErrCodeForbidden {
			t.Errorf("Origin %q: %s", origin, w.Body)
		}
	}
}

// A preflight carrying the MCP headers from a listed origin succeeds (P7.3 acceptance).
func TestMCPPreflightFromAListedOrigin(t *testing.T) {
	e := newMCPEnv(t)
	w := req{method: http.MethodOptions, path: "/mcp", raw: true, header: map[string]string{
		"Origin": "https://inspector.example.com", "Access-Control-Request-Method": "POST",
		"Access-Control-Request-Headers": "authorization, content-type, mcp-protocol-version, mcp-method, mcp-name",
	}}.do(e.s.Handler())
	allow := w.Header().Get("Access-Control-Allow-Headers")
	if w.Code != http.StatusNoContent || w.Header().Get("Access-Control-Allow-Origin") != "https://inspector.example.com" ||
		!strings.Contains(allow, "mcp-protocol-version") || !strings.Contains(allow, "mcp-method") || !strings.Contains(allow, "mcp-name") ||
		w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Errorf("preflight: %d %v", w.Code, w.Header())
	}
	// The answer to the token's request itself is readable by that origin.
	got := e.post(withBearer(e.mcpTok, "Origin", "https://inspector.example.com"))
	if got.Header().Get("Access-Control-Allow-Origin") != "https://inspector.example.com" {
		t.Errorf("response headers: %v", got.Header())
	}
}

// Every MCP call is audited (design N.7): surface mcp, the token, the tool, the SHA-256 of the
// arguments and never the arguments, the outcome and the duration.
func TestMCPCallsAreAudited(t *testing.T) {
	a, err := audit.Open(t.TempDir(), audit.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	e := newMCPEnv(t, func(d *Deps) { d.Audit = a })
	const args = `{"query":"canary-words-7f3a"}`
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dash_search","arguments":` + args + `}}`
	w := req{method: http.MethodPost, path: "/mcp", body: body, raw: true, header: withBearer(e.mcpTok,
		"Content-Type", "application/json", "Accept", "application/json, text/event-stream",
		"MCP-Protocol-Version", "2025-11-25", "X-Dash-Csrf", "", "Origin", "")}.do(e.s.Handler())
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call: %d %s", w.Code, w.Body)
	}
	recs, _, _ := a.Recent(20, "", audit.RingMain)
	i := slices.IndexFunc(recs, func(r audit.Record) bool { return r.Action == "mcp tools/call" })
	if i < 0 {
		t.Fatalf("no record in %+v", recs)
	}
	rec := recs[i]
	sum := sha256.Sum256([]byte(args))
	if rec.Actor.Surface != "mcp" || rec.Actor.TokenID == "" || rec.Target.Kind != "tool" || rec.Target.Name != "dash_search" ||
		rec.ArgsSHA256 != hex.EncodeToString(sum[:]) || rec.Result != audit.ResultOK || rec.Kind != audit.KindEvent {
		t.Errorf("record = %+v", rec)
	}
	raw, _ := json.Marshal(recs)
	if strings.Contains(string(raw), "canary-words") || strings.Contains(string(raw), e.mcpTok) {
		t.Errorf("the arguments or the token reached the audit log: %s", raw)
	}
}

func decodeErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var e model.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q: %v", w.Body, err)
	}
	return e.Code
}
