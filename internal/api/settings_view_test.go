package api

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/api/settingstest"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
)

// Every field says which layer its value comes from, what the store holds, and why the UI may
// not write it: the file or env layer first, then ui-ineligible, then a read-only store.
func TestSettingsViewCarriesProvenance(t *testing.T) {
	o := settingstest.Options{
		File: "plugins:\n  grafana:\n    url: https://grafana.example.com\n",
		Env:  []string{"DASH_PLUGINS_HEADLAMP_URL=https://headlamp.example.com"},
		Stored: map[string]any{
			"plugins.proxmox.node": "pve", "plugins.grafana.url": "https://stored.example.com",
		},
	}
	e := newSettingsEnv(t, o)
	w := e.do(http.MethodGet, "/api/v1/settings", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	view := decodeBody[model.SettingsView](t, w)
	if view.Revision != e.f.St.Revision() || view.RestartRequired == nil || view.Store.Readonly {
		t.Errorf("view header = rev %d, restart %v, store %+v", view.Revision, view.RestartRequired, view.Store)
	}

	for _, tc := range []struct {
		id, path string
		want     model.SettingField
	}{
		{"grafana", "plugins.grafana.url", model.SettingField{Source: "file", Ref: e.f.ConfigPath, Locked: true, LockedBy: "file", Shadowed: true, Stored: true, Set: true, Value: "https://grafana.example.com"}},
		{"headlamp", "plugins.headlamp.url", model.SettingField{Source: "env", Ref: "DASH_PLUGINS_HEADLAMP_URL", Locked: true, LockedBy: "env", Set: true, Value: "https://headlamp.example.com"}},
		{"proxmox", "plugins.proxmox.node", model.SettingField{Source: "ui", Stored: true, Set: true, Value: "pve"}},
		{"gatus", "plugins.gatus.url", model.SettingField{Source: "default", Value: ""}},
	} {
		got := field(t, e.pluginView(t, tc.id).Fields, tc.path)
		got.Type, got.Doc, got.Restart, got.Enum = "", "", false, nil
		tc.want.Path = tc.path
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.path, got, tc.want)
		}
	}
	for path, lock := range map[string]model.SettingLock{
		"server.listen": "ui-ineligible", "auth.proxy.adminUsers": "ui-ineligible", "settings.ui": "ui-ineligible",
		"settings.secretNamespaces": "ui-ineligible", "ui.iconBaseURL": "",
		"dataDir": "env", "auth.mode": "env", // the fake's environment sets them: env comes first
	} {
		if f := field(t, view.General, path); f.LockedBy != lock || f.Locked != (lock != "") {
			t.Errorf("%s: locked %v by %q, want %q", path, f.Locked, f.LockedBy, lock)
		}
	}
	if f := field(t, view.General, "server.csp"); f.Type != "string" || !slices.Equal(f.Enum, []string{"enforce", "report-only", "off"}) || !f.Restart || f.Doc == "" {
		t.Errorf("server.csp metadata = %+v", f)
	}
	i := slices.IndexFunc(view.Plugins, func(p model.SettingsPlugin) bool { return p.ID == "proxmox" })
	if i < 0 || view.Plugins[i].Enabled.Path != "plugins.proxmox.enabled" || view.Plugins[i].Enabled.Type != "boolean" {
		t.Errorf("proxmox row = %+v", view.Plugins)
	}

	// A read-only store locks what nothing else does; a file or env lock names its layer first.
	ro := newSettingsEnv(t, settingstest.Options{ReadOnly: true, Env: o.Env})
	if f := field(t, ro.pluginView(t, "proxmox").Fields, "plugins.proxmox.node"); f.LockedBy != "readonly" {
		t.Errorf("read-only node locked by %q", f.LockedBy)
	}
	if f := field(t, ro.pluginView(t, "headlamp").Fields, "plugins.headlamp.url"); f.LockedBy != "env" {
		t.Errorf("read-only env url locked by %q", f.LockedBy)
	}
	gen := decodeBody[model.SettingsView](t, ro.do(http.MethodGet, "/api/v1/settings", nil, nil)).General
	if f := field(t, gen, "server.listen"); f.LockedBy != "ui-ineligible" {
		t.Errorf("read-only server.listen locked by %q", f.LockedBy)
	}
}

// A URL is shown without its userinfo, whatever layer it comes from.
func TestSettingsViewRedactsURLUserinfo(t *testing.T) {
	const canary = "pw-canary-7c1e"
	e := newSettingsEnv(t, settingstest.Options{
		Env: []string{"DASH_PLUGINS_GRAFANA_URL=https://admin:" + canary + "@grafana.example.com/g"},
	})
	w := e.do(http.MethodGet, "/api/v1/settings/plugins/grafana", nil, nil)
	if strings.Contains(w.Body.String(), canary) || strings.Contains(w.Body.String(), "admin:") {
		t.Fatalf("userinfo in the view: %s", w.Body.String())
	}
	if f := field(t, decodeBody[model.SettingsPluginView](t, w).Fields, "plugins.grafana.url"); f.Value != "https://grafana.example.com/g" {
		t.Errorf("value = %v", f.Value)
	}
	if w := e.do(http.MethodGet, "/api/v1/settings", nil, nil); strings.Contains(w.Body.String(), canary) {
		t.Errorf("userinfo in the settings view")
	}
}

// The plugin view carries the plugin's own schema document, the one the web fixtures hold.
func TestSettingsSchemaServesThePluginSubschema(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{})
	for _, id := range []string{"proxmox", "hardware"} {
		var got, want any
		if err := json.Unmarshal(e.pluginView(t, id).Schema, &got); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile("../../web/src/settings/fixtures/" + id + ".schema.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: the served schema differs from the fixture", id)
		}
	}
	// The whole configuration's schema is the one `dash config schema` prints.
	w := e.do(http.MethodGet, "/api/v1/settings/schema", nil, nil)
	doc, err := config.SchemaDocument()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(doc)
	var got, want any
	_ = json.Unmarshal(b, &want)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("GET /settings/schema is not the configuration schema (%v)", err)
	}
}

func TestUnknownPluginIs404(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true})
	for _, rq := range []struct{ method, path string }{
		{"GET", "/api/v1/settings/plugins/nope"},
		{"PUT", "/api/v1/settings/plugins/nope"},
		{"POST", "/api/v1/settings/plugins/nope/test"},
		{"PUT", "/api/v1/settings/plugins/nope/secrets/tokenSecret"},
		{"DELETE", "/api/v1/settings/plugins/nope/secrets/tokenSecret"},
		{"DELETE", "/api/v1/settings/plugins/nope/stored/url"},
		{"PUT", "/api/v1/settings/plugins/proxmox/secrets/a%2Fb"},
		{"DELETE", "/api/v1/settings/plugins/proxmox/stored/tls%2FcaFile"},
	} {
		w := e.do(rq.method, rq.path, map[string]any{"value": "x"}, e.ifMatch())
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d %s", rq.method, rq.path, w.Code, w.Body.String())
		}
	}
}

// settingsRequests is one request per /api/v1/settings/** row of the route table.
func settingsRequests(t *testing.T, s *Server) []req {
	t.Helper()
	var out []req
	for _, r := range s.routeTable() {
		if !strings.HasPrefix(r.Path, "/api/v1/settings") {
			continue
		}
		if r.Role != "admin" || !r.Interactive {
			t.Errorf("%s %s: role %q interactive %v", r.Method, r.Path, r.Role, r.Interactive)
		}
		path := strings.NewReplacer("{id}", "proxmox", "{field}", "tokenSecret").Replace(r.Path)
		out = append(out, req{method: r.Method, path: path, body: map[string]any{}, header: map[string]string{"If-Match": `"0"`}})
	}
	if len(out) < 11 {
		t.Fatalf("%d settings routes, want 11", len(out))
	}
	return out
}

// The epic's "a viewer gets 403 on /api/settings/*": every settings route of the table.
func TestViewerGets403OnEverySettingsRoute(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{SecretKey: true})
	for _, rq := range settingsRequests(t, e.s) {
		rq.header["X-Forwarded-Preferred-Username"] = "bob" // not an administrator
		w := rq.do(e.h)
		if w.Code != http.StatusForbidden || decodeBody[model.APIError](t, w).Code != model.ErrCodeForbidden {
			t.Errorf("%s %s as a viewer = %d %s", rq.method, rq.path, w.Code, w.Body.String())
		}
	}
	if rev := e.f.St.Revision(); rev != 0 {
		t.Errorf("a viewer's request wrote: revision %d", rev)
	}
}

// Demo mode has no settings store: every settings route is 404.
func TestSettingsRoutesAre404InDemoMode(t *testing.T) {
	s := newServer(nil)
	h := s.Handler()
	for _, rq := range settingsRequests(t, s) {
		w := rq.do(h)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "demo mode") {
			t.Errorf("%s %s in demo mode = %d %s", rq.method, rq.path, w.Code, w.Body.String())
		}
	}
}

// A changed server.listen in the config file is listed as needing a restart.
func TestRestartRequiredAfterAFileChangeOfAServerKey(t *testing.T) {
	e := newSettingsEnv(t, settingstest.Options{File: "server:\n  listen: 127.0.0.1:8080\n"})
	e.f.WriteConfig(t, "server:\n  listen: 127.0.0.1:9090\n")
	if err := e.f.WithLock(e.f.Reload); err != nil {
		t.Fatal(err)
	}
	view := decodeBody[model.SettingsView](t, e.do(http.MethodGet, "/api/v1/settings", nil, nil))
	if !slices.Contains(view.RestartRequired, "server.listen") {
		t.Errorf("restartRequired = %v", view.RestartRequired)
	}
}

// An enabled field no layer sets shows what runs: true for a running instance, false for one
// the supervisor disabled, absent without a live row. It stays unset, from the default; a set
// value is shown as set. The GET /settings row and the plugin view agree.
func TestUnsetEnabledShowsTheRunningState(t *testing.T) {
	infos := []model.PluginInfo{
		{ID: "gatus", Name: "Gatus", State: model.PluginHealthy},
		{ID: "proxmox", Name: "Proxmox", State: model.PluginDisabled},
		{ID: "grafana", Name: "Grafana", State: model.PluginHealthy},
	}
	e := newSettingsEnv(t, settingstest.Options{Stored: map[string]any{"plugins.grafana.enabled": false}}, func(d *Deps) {
		d.Plugins = livePlugins{PluginsAPI: emptyPlugins(), infos: infos}
	})
	view := decodeBody[model.SettingsView](t, e.do(http.MethodGet, "/api/v1/settings", nil, nil))
	rows := map[string]model.SettingField{}
	for _, p := range view.Plugins {
		rows[p.ID] = p.Enabled
	}
	for id, want := range map[string]model.SettingField{
		"gatus":    {Value: true, Source: model.SettingSourceDefault},
		"proxmox":  {Value: false, Source: model.SettingSourceDefault},
		"grafana":  {Value: false, Set: true, Source: model.SettingSourceUI},
		"headlamp": {Source: model.SettingSourceDefault}, // no live row: no value
	} {
		for where, got := range map[string]model.SettingField{
			"row":  rows[id],
			"view": field(t, e.pluginView(t, id).Fields, "plugins."+id+".enabled"),
		} {
			if got.Value != want.Value || got.Set != want.Set || got.Source != want.Source {
				t.Errorf("%s %s: value %v set %v source %q, want %v %v %q", id, where, got.Value, got.Set, got.Source, want.Value, want.Set, want.Source)
			}
		}
	}
}

// livePlugins answers Infos with fixed rows; the rest is a supervisor without plugins.
type livePlugins struct {
	PluginsAPI
	infos []model.PluginInfo
}

func (p livePlugins) Infos() []model.PluginInfo { return p.infos }
