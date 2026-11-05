package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/audit"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/auth/authtest"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/overview"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/secret"
)

// uiPVEURL is the Proxmox address an admin types into the settings page. A URL saved from the UI
// refuses loopback at dial, so the fake upstream (on 127.0.0.1) cannot be named directly; the
// framework's transport hooks route this host to it instead (routeHost). The dial guard is not
// what these tests are about.
const uiPVEURL = "https://pve.example.com:8006"

// newUIPVE is a fake Proxmox API behind TLS: newFakePVE's node and VM (with the Authorization
// header of every request recorded in the returned fakePVE) plus /api2/json/version, which the
// connection check reads. versions counts the check's reads.
func newUIPVE(t *testing.T) (pve *fakePVE, srv *httptest.Server, versions *atomic.Int32) {
	t.Helper()
	pve = newFakePVE(t)
	versions = new(atomic.Int32)
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api2/json/version" {
			versions.Add(1)
			_, _ = io.WriteString(w, `{"data":{"version":"9.0.5","release":"9.0","repoid":"0123abcd"}}`)
			return
		}
		pve.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return pve, srv, versions
}

// routeHost is a transport hook of newFramework that sends every request for the host of from
// to target's listener, over target's own client transport (which trusts its certificate), and
// leaves every other request to the plugin's guarded transport.
func routeHost(t *testing.T, from string, target *httptest.Server) func(plugin.ID, http.RoundTripper) http.RoundTripper {
	t.Helper()
	src, err := url.Parse(from)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := url.Parse(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	via := target.Client().Transport
	return func(_ plugin.ID, next http.RoundTripper) http.RoundTripper {
		return hostRoute{host: src.Host, dst: dst, via: via, next: next}
	}
}

type hostRoute struct {
	host      string
	dst       *url.URL
	via, next http.RoundTripper
}

func (h hostRoute) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != h.host {
		return h.next.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.URL.Scheme, r.URL.Host, r.Host = h.dst.Scheme, h.dst.Host, ""
	return h.via.RoundTrip(r)
}

// uiServer is the API of a running dash as run wires it outside demo mode: the settings store
// and the audit log of a writable data directory with a secret key, the plugin framework with
// its reloader running, and api.New with the settings backend. The proxy authenticator makes
// alice the admin; anyone else is a viewer.
type uiServer struct {
	dir     string
	res     *config.Resolved
	fw      *framework
	handler http.Handler
	logs    *lockedBuf
	bodies  []string // every response body, in order
}

// startUIServer starts the plugins of factories with the given transport hooks (nil: none) on
// the environment of a production start; env adds to it.
func startUIServer(t *testing.T, factories []plugin.Factory, transport, check func(plugin.ID, http.RoundTripper) http.RoundTripper, env ...string) *uiServer {
	t.Helper()
	cleanEnv(t)
	dir := t.TempDir()
	startEnv(t, dir, append([]string{
		"DASH_SECRET_KEY=" + testKey(7),
		// No kubeconfig and no in-cluster environment: the kubernetes plugin has no cluster.
		"KUBECONFIG=", "KUBERNETES_SERVICE_HOST=", "KUBERNETES_SERVICE_PORT=",
	}, env...)...)
	logs := &lockedBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	reg := secret.NewRegistry()
	res, store, alog, err := openState(reg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		t.Fatal("no settings store outside demo mode")
	}
	// Cleanups run last-in first-out: the plugins and the reloader stop before the log and the
	// store close, as in run.
	t.Cleanup(func() { _ = alog.Close(); _ = store.Close() })
	fw := newFramework(res, store, config.ReloadOptions{}, reg, nil, transport, check, false)
	ctx, cancel := context.WithCancel(context.Background())
	ov := overview.New(overview.Deps{Hosts: fw.sup, Contributors: fw.sup.Contributors})
	if err := fw.start(ctx, factories, func() { ov.Run(ctx) }); err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); fw.reloader.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer scancel()
		fw.sup.Shutdown(sctx)
	})

	srv := api.New(api.Deps{
		Cfg: res.Config, Version: "test", Hub: fw.hub, Plugins: fw.sup, Health: fw.sup, Secrets: reg, Audit: alog,
		Auth:     authtest.Proxy("alice"),
		Settings: newSettingsBackend(res, store, fw.reloader, fw.sup, alog),
	})
	return &uiServer{dir: dir, res: res, fw: fw, handler: srv.Handler(), logs: logs}
}

// do sends one request as user from the identity proxy next to dash. A request that is not a
// GET carries the CSRF header and the public origin, as the SPA's do; hdr adds key, value pairs.
func (u *uiServer) do(t *testing.T, user, method, path string, body any, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, u.res.Config.PublicURL+path, rd)
	r.RemoteAddr = "127.0.0.1:41000"
	r.Header.Set("X-Forwarded-Preferred-Username", user)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if method != http.MethodGet {
		r.Header.Set("X-Dash-Csrf", "1")
		r.Header.Set("Origin", u.res.Config.PublicOrigin())
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	u.handler.ServeHTTP(w, r)
	u.bodies = append(u.bodies, w.Body.String())
	return w
}

// ok decodes the body of a 200 into out.
func ok(t *testing.T, what string, w *httptest.ResponseRecorder, out any) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("%s = %d %s", what, w.Code, w.Body)
	}
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v in %s", what, err, w.Body)
		}
	}
}

// pluginRow is the row of plugin id in core.plugins as the API serves the topic.
func (u *uiServer) pluginRow(t *testing.T, id string) (model.PluginInfo, bool) {
	t.Helper()
	w := u.do(t, "alice", http.MethodGet, "/api/v1/topics/"+string(model.TopicPlugins), nil)
	var env model.Envelope
	var data model.PluginsData
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &env) != nil || json.Unmarshal(env.Data, &data) != nil {
		return model.PluginInfo{}, false
	}
	for _, p := range data.Plugins {
		if p.ID == id {
			return p, true
		}
	}
	return model.PluginInfo{}, false
}

// auditFile is every record of the audit log file of dir, in order.
func auditFile(t *testing.T, dir string) ([]audit.Record, string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, audit.FileName))
	if err != nil {
		t.Fatal(err)
	}
	var recs []audit.Record
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		var rec audit.Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("audit line %q: %v", sc.Text(), err)
		}
		recs = append(recs, rec)
	}
	return recs, string(raw)
}

// The group's acceptance: an admin configures Proxmox from the settings page against a running
// dash, and the running plugin picks it up with nothing restarted. The first save sends the URL,
// node, token ID and token secret in one patch (the token ID without its secret does not
// validate, and a patch that changes where requests go may carry its secrets); the secret is then
// rotated through the secrets endpoint, the saved configuration is tested, and the proxmox row of
// core.plugins turns healthy with the rotated token on its requests. The audit log holds an
// intent and an ok outcome (same ID) for each write and a settings.test event; no record,
// response body, log line or file in the data directory holds either secret.
func TestGroupAcceptanceAdminConfiguresAPluginFromTheUI(t *testing.T) {
	pve, upstream, versions := newUIPVE(t)
	route := routeHost(t, uiPVEURL, upstream)
	u := startUIServer(t, registry(nil), route, route)
	const (
		firstSecret = "pve-first-secret-0123456789"
		finalSecret = "pve-rotated-secret-9876543210"
	)
	if row, _ := u.pluginRow(t, "proxmox"); row.State == model.PluginHealthy {
		t.Fatalf("proxmox is healthy before it is configured: %+v", row)
	}

	var view model.SettingsView
	ok(t, "GET /api/v1/settings", u.do(t, "alice", http.MethodGet, "/api/v1/settings", nil), &view)
	if !view.Store.Writable || !view.Store.SecretKey || view.Store.Readonly {
		t.Fatalf("store = %+v, want writable with a secret key", view.Store)
	}
	ifMatch := func(rev uint64) []string { return []string{"If-Match", `"` + strconv.FormatUint(rev, 10) + `"`} }

	var saved model.SettingsPatchResult
	ok(t, "PUT plugins/proxmox", u.do(t, "alice", http.MethodPut, "/api/v1/settings/plugins/proxmox", map[string]any{
		"enabled": true, "url": uiPVEURL, "node": "pve", "tokenID": "u@pve!t", "tokenSecret": firstSecret,
	}, ifMatch(view.Revision)...), &saved)
	if saved.Revision <= view.Revision || len(saved.RestartRequired) != 0 {
		t.Fatalf("after the save: %+v (read at revision %d), want a newer revision and no restart: the plugin applies it live", saved, view.Revision)
	}

	var rotated model.SettingsPatchResult
	ok(t, "PUT secrets/tokenSecret", u.do(t, "alice", http.MethodPut, "/api/v1/settings/plugins/proxmox/secrets/tokenSecret",
		model.SettingsSecretWrite{Value: finalSecret}, ifMatch(saved.Revision)...), &rotated)
	if rotated.Revision <= saved.Revision || len(rotated.RestartRequired) != 0 {
		t.Fatalf("after the rotation: %+v, want a newer revision than %d and no restart", rotated, saved.Revision)
	}

	var report model.CheckReport
	ok(t, "POST test", u.do(t, "alice", http.MethodPost, "/api/v1/settings/plugins/proxmox/test", map[string]any{}), &report)
	if len(report.Checks) == 0 {
		t.Fatal("the test ran no check")
	}
	for _, c := range report.Checks {
		if !c.OK {
			t.Errorf("check %+v failed", c)
		}
	}
	if versions.Load() == 0 {
		t.Error("the connection test did not reach the fake Proxmox")
	}

	// The running plugin: the row turns healthy, and its next request to Proxmox (the detail of
	// VM 101, asked for now) carries the rotated token, with the same process and the same
	// framework that were running before the first save.
	waitFor(t, func() bool {
		row, _ := u.pluginRow(t, "proxmox")
		return row.State == model.PluginHealthy
	})
	waitFor(t, func() bool {
		return u.do(t, "alice", http.MethodGet, "/api/v1/plugins/proxmox/vms/101", nil).Code == http.StatusOK
	})
	if got, want := pve.lastAuth(), "PVEAPIToken=u@pve!t="+finalSecret; got != want {
		t.Errorf("the plugin's last request carried %q, want %q", got, want)
	}

	// The audit log file: every intent has its outcome under the same ID, one pair per write.
	recs, raw := auditFile(t, u.dir)
	intents, outcomes := map[string]audit.Record{}, map[string]audit.Record{}
	pairs := map[string]int{}
	tests := 0
	for _, rec := range recs {
		switch rec.Kind {
		case audit.KindIntent:
			intents[rec.ID] = rec
		case audit.KindOutcome:
			outcomes[rec.ID] = rec
		case audit.KindEvent:
			if rec.Action == "settings.test" {
				tests++
				if rec.Plugin != "proxmox" || rec.Result != audit.ResultOK || rec.Actor.User != "alice" {
					t.Errorf("settings.test event = %+v", rec)
				}
			}
		}
	}
	for id, in := range intents {
		out, found := outcomes[id]
		if !found {
			t.Errorf("intent %+v has no outcome", in)
			continue
		}
		if out.Action != in.Action || out.Result != audit.ResultOK || in.Actor.User != "alice" {
			t.Errorf("intent %+v, outcome %+v", in, out)
		}
		pairs[in.Action]++
	}
	for id := range outcomes {
		if _, found := intents[id]; !found {
			t.Errorf("outcome %s has no intent", id)
		}
	}
	if pairs["settings.set"] != 1 || pairs["settings.secret.replace"] != 1 || tests != 1 {
		t.Errorf("intent/outcome pairs = %v, settings.test events = %d; want one settings.set, one settings.secret.replace and one test\n%s",
			pairs, tests, raw)
	}
	for _, in := range intents {
		if in.Action == "settings.set" && !slices.Contains(in.Fields, "plugins.proxmox.tokenSecret") {
			t.Errorf("settings.set names %v, want the secret's path among them", in.Fields)
		}
	}

	// The audit view of the settings page shows the same three actions.
	var page model.SettingsAudit
	ok(t, "GET /api/v1/settings/audit", u.do(t, "alice", http.MethodGet, "/api/v1/settings/audit?ring=main", nil), &page)
	seen := map[string]bool{}
	for _, e := range page.Records {
		seen[e.Action] = true
	}
	if !page.Enabled || !seen["settings.set"] || !seen["settings.secret.replace"] || !seen["settings.test"] {
		t.Errorf("audit view enabled %v with actions %v", page.Enabled, seen)
	}

	// Neither secret is in a record, an answer, a log line, or any file of the data directory
	// (the store keeps the secret sealed).
	files := map[string]string{}
	if err := filepath.WalkDir(u.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		files[path] = string(b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Errorf("data directory holds %d files, want the store and the audit log", len(files))
	}
	for _, s := range []string{firstSecret, finalSecret} {
		if strings.Contains(raw, s) {
			t.Errorf("an audit record holds a secret")
		}
		for i, b := range u.bodies {
			if strings.Contains(b, s) {
				t.Errorf("response %d holds a secret: %s", i, b)
			}
		}
		if strings.Contains(u.logs.String(), s) {
			t.Errorf("the log holds a secret")
		}
		for path, content := range files {
			if strings.Contains(content, s) {
				t.Errorf("%s holds a secret in plain text", path)
			}
		}
	}
}

// adminOperations is every operation the OpenAPI document of this build (what `dash api openapi`
// prints: the route table with every shipped plugin's actions) marks x-dash-min-role admin, as
// "METHOD /path" with the path parameters filled in: id proxmox, field tokenSecret.
func adminOperations(t *testing.T) []string {
	t.Helper()
	doc, err := api.OpenAPI(builtinManifests(), builtinRoutes)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(doc, &spec); err != nil {
		t.Fatal(err)
	}
	fill := strings.NewReplacer("{id}", "proxmox", "{field}", "tokenSecret")
	var out []string
	for path, ops := range spec.Paths {
		for method, raw := range ops {
			var op struct {
				Role string `json:"x-dash-min-role"`
			}
			if json.Unmarshal(raw, &op) != nil || op.Role != string(auth.RoleAdmin) {
				continue
			}
			out = append(out, strings.ToUpper(method)+" "+fill.Replace(path))
		}
	}
	slices.Sort(out)
	return out
}

// No admin route answers a viewer: every operation with the admin role (the ten settings routes
// and the plugin actions, the mounted alertmanager silence.create among them) is 403 forbidden
// for vera, behind the CSRF header and the public origin so the refusal is the role's. The action
// never runs and the store does not move. The same requests from the admin are not refused.
func TestGroupAcceptanceViewerReachesNeitherSettingsNorActions(t *testing.T) {
	am := alertmanagerFake()
	var acts atomic.Int32
	am.ActionFn = func(context.Context, string, auth.Principal, any) (any, error) { acts.Add(1); return nil, nil }
	u := startUIServer(t, []plugin.Factory{am.Factory()}, nil, nil,
		"DASH_PLUGINS_ALERTMANAGER_URL=http://alertmanager.example", "DASH_PLUGINS_ALERTMANAGER_ACTIONS_ENABLED=true")
	waitFor(t, func() bool { return mounted(u.fw, "alertmanager", "silence.create") })

	var me model.Me
	ok(t, "GET /api/v1/me", u.do(t, "vera", http.MethodGet, "/api/v1/me", nil), &me)
	if me.Role != string(auth.RoleViewer) {
		t.Fatalf("vera is %q, want a viewer", me.Role)
	}
	var before model.SettingsView
	ok(t, "GET /api/v1/settings as the admin", u.do(t, "alice", http.MethodGet, "/api/v1/settings", nil), &before)

	ops := adminOperations(t)
	// The document covers the route table: every settings route of the table is an admin
	// operation of it (and nothing else under /api/v1/settings is), and so is the mounted action.
	fill := strings.NewReplacer("{id}", "proxmox", "{field}", "tokenSecret")
	var table []string
	for _, p := range api.RoutePatterns() {
		if strings.Contains(p, " /api/v1/settings") {
			table = append(table, fill.Replace(p))
		}
	}
	slices.Sort(table)
	settingsOps := slices.DeleteFunc(slices.Clone(ops), func(op string) bool { return !strings.Contains(op, " /api/v1/settings") })
	if len(table) == 0 || !slices.Equal(settingsOps, table) {
		t.Fatalf("admin settings operations %v, settings routes of the table %v", settingsOps, table)
	}
	if !slices.Contains(ops, "POST /api/v1/plugins/alertmanager/actions/silence.create") {
		t.Fatalf("admin operations = %v, want the alertmanager actions among them", ops)
	}

	send := func(user, op string) *httptest.ResponseRecorder {
		method, path, _ := strings.Cut(op, " ")
		var body any
		if method != http.MethodGet {
			body = map[string]any{}
		}
		return u.do(t, user, method, path, body, "If-Match", `"`+strconv.FormatUint(before.Revision, 10)+`"`)
	}
	for _, op := range ops {
		w := send("vera", op)
		var e model.APIError
		if w.Code != http.StatusForbidden || json.Unmarshal(w.Body.Bytes(), &e) != nil || e.Code != model.ErrCodeForbidden {
			t.Errorf("%s as a viewer = %d %s, want 403 forbidden", op, w.Code, w.Body)
		}
	}
	if n := acts.Load(); n != 0 {
		t.Errorf("the action ran %d times for a viewer", n)
	}
	var after model.SettingsView
	ok(t, "GET /api/v1/settings as the admin", u.do(t, "alice", http.MethodGet, "/api/v1/settings", nil), &after)
	if after.Revision != before.Revision {
		t.Errorf("the store moved from revision %d to %d", before.Revision, after.Revision)
	}

	// The control: the refusal is the role's, not the route's.
	for _, op := range ops {
		if w := send("alice", op); w.Code == http.StatusForbidden {
			t.Errorf("%s as the admin = %d %s", op, w.Code, w.Body)
		}
	}
}

// Demo mode has no settings API: run in demo mode (no data directory, so no store, so run wires
// no settings backend) answers GET /api/v1/settings with 404 not_found to an admin.
func TestDemoModeHasNoSettingsAPI(t *testing.T) {
	cleanEnv(t)
	startEnv(t, filepath.Join(t.TempDir(), "data"), "DASH_DEMO=true", `DASH_AUTH_PROXY_ADMIN_USERS=["alice"]`,
		"KUBECONFIG=", "KUBERNETES_SERVICE_HOST=", "KUBERNETES_SERVICE_PORT=",
		"DASH_SERVER_LISTEN="+anyPort, "DASH_SERVER_HEALTH_LISTEN="+anyPort)
	type addrs struct{ api, health string }
	bound := make(chan addrs, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(secret.NewRegistry(), func(a, h net.Addr) { bound <- addrs{a.String(), h.String()} })
	}()
	var a addrs
	select {
	case a = <-bound:
	case err := <-done:
		t.Fatalf("run returned before binding: %v", err)
	}
	// run installs its signal handler before it serves: SIGTERM only once /healthz has answered.
	defer func() {
		waitFor(t, func() bool { c, _ := fetch(a.health, "/healthz", false); return c == http.StatusOK })
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Error(err)
			return
		}
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("run returned %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("run did not return after SIGTERM")
		}
	}()

	code, body := fetch(a.api, "/api/v1/me", true)
	var me model.Me
	if code != http.StatusOK || json.Unmarshal(body, &me) != nil || me.Role != string(auth.RoleAdmin) {
		t.Fatalf("/api/v1/me = %d %s, want alice as an admin", code, body)
	}
	code, body = fetch(a.api, "/api/v1/settings", true)
	var e model.APIError
	if code != http.StatusNotFound || json.Unmarshal(body, &e) != nil || e.Code != model.ErrCodeNotFound {
		t.Errorf("GET /api/v1/settings in demo mode = %d %s, want 404 not_found", code, body)
	}
}
