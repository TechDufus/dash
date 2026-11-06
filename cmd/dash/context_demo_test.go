package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/api"
	"github.com/TechDufus/dash/internal/auth"
	"github.com/TechDufus/dash/internal/bundle"
	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/secret"
)

// contextGet asks GET /api/v1/context of a demo run as p.
func contextGet(t *testing.T, fw *framework, cfg config.Config, p auth.Principal, query url.Values) *httptest.ResponseRecorder {
	t.Helper()
	h := api.New(api.Deps{Cfg: cfg, Hub: fw.hub, Plugins: fw.sup, Health: fw.sup, Auth: auth.Fixed(p)}).Handler()
	r := httptest.NewRequest(http.MethodGet, cfg.PublicURL+"/api/v1/context?"+query.Encode(), nil)
	r.RemoteAddr = "127.0.0.1:41000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

var demoAdmin = auth.Principal{Kind: auth.KindDemo, User: "demo", Role: auth.RoleAdmin}

// attentionPod is a demo pod the overview raises an attention item for.
func attentionPod(t *testing.T, fw *framework) string {
	t.Helper()
	d, _ := overviewOf(t, fw)
	for _, a := range d.Attention {
		if strings.HasPrefix(a.Ref, "kubernetes:Pod/") {
			return a.Ref
		}
	}
	t.Fatal("no attention item names a demo pod")
	return ""
}

func decodeBundle(t *testing.T, w *httptest.ResponseRecorder) model.ContextBundle {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var b model.ContextBundle
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// The demo pod bundle (P7.4): its status, at most 20 events, the attention items for it, the
// summaries of its CPU and memory series, and at most 100 log lines for a caller with logs.
func TestDemoPodContextBundle(t *testing.T) {
	fw, cfg := startDemo(t)
	awaitDemoTopics(t, fw)
	ref := attentionPod(t, fw)
	b := decodeBundle(t, contextGet(t, fw, cfg, demoAdmin, url.Values{"ref": {ref}}))
	if b.Ref != ref || b.Part.Kind != "Pod" || b.Part.Summary == "" || len(b.Part.Conditions) == 0 {
		t.Errorf("part = %+v", b.Part)
	}
	if len(b.Part.Events) > model.ContextMaxEvents {
		t.Errorf("%d events", len(b.Part.Events))
	}
	if len(b.Attention) == 0 {
		t.Error("no attention item")
	}
	for _, a := range b.Attention {
		if a.Ref != ref {
			t.Errorf("attention item of another object: %+v", a)
		}
	}
	if len(b.Series) != 2 || b.Series[0].Name != "pod.cpu" || b.Series[0].Error != "" || len(b.Series[0].Lines) == 0 {
		t.Errorf("series = %+v", b.Series)
	}
	if len(b.Logs) == 0 || len(b.Logs) > model.ContextMaxLogLines || b.LogsNote != "" {
		t.Errorf("%d log lines, note %q", len(b.Logs), b.LogsNote)
	}
}

// Log lines need the logs scope: a read-only token gets the rest of the bundle and none.
func TestDemoContextBundleWithoutTheLogsScope(t *testing.T) {
	fw, cfg := startDemo(t)
	awaitDemoTopics(t, fw)
	ref := attentionPod(t, fw)
	bot := auth.Principal{Kind: auth.KindToken, User: "bot", Role: auth.RoleViewer, Audience: "api", Scopes: []string{"read"}}
	b := decodeBundle(t, contextGet(t, fw, cfg, bot, url.Values{"ref": {ref}}))
	if len(b.Logs) != 0 || b.LogsNote != "logs_scope_required" || len(b.Series) != 2 {
		t.Errorf("logs %d, note %q, series %d", len(b.Logs), b.LogsNote, len(b.Series))
	}
	w := contextGet(t, fw, cfg, bot, url.Values{"ref": {ref}, "format": {"markdown"}})
	if strings.Contains(w.Body.String(), "\nlogs of ") || !strings.Contains(w.Body.String(), "No log lines: the logs scope is required.") {
		t.Errorf("markdown:\n%s", w.Body)
	}
	if w := contextGet(t, fw, cfg, auth.Principal{Kind: auth.KindToken, User: "bot", Role: auth.RoleViewer, Scopes: []string{"logs"}}, url.Values{"ref": {ref}}); w.Code != http.StatusForbidden {
		t.Errorf("a token without read: %d", w.Code)
	}
}

// The victorialogs deny list holds for bundles: a denied namespace gives no log lines.
func TestDemoContextBundleHonoursTheLogsDenyList(t *testing.T) {
	fw, cfg := startDemo(t)
	awaitDemoTopics(t, fw)
	ref := attentionPod(t, fw)
	_, _, id, _ := model.ParseRef(ref)
	ns, _, _ := strings.Cut(id, "/")
	fw2, cfg2 := startDemo(t, `DASH_PLUGINS_VICTORIALOGS_DENY_NAMESPACES=["`+ns+`"]`)
	awaitDemoTopics(t, fw2)
	b := decodeBundle(t, contextGet(t, fw2, cfg2, demoAdmin, url.Values{"ref": {ref}}))
	if len(b.Logs) != 0 || b.LogsNote != "namespace_denied" {
		t.Errorf("logs %d, note %q", len(b.Logs), b.LogsNote)
	}
	if b := decodeBundle(t, contextGet(t, fw, cfg, demoAdmin, url.Values{"ref": {ref}})); len(b.Logs) == 0 {
		t.Error("the run without a deny list has no log lines either")
	}
}

// Markdown: the lab data, the log lines among it, sits inside the untrusted block only.
func TestDemoContextBundleMarkdown(t *testing.T) {
	fw, cfg := startDemo(t)
	awaitDemoTopics(t, fw)
	ref := attentionPod(t, fw)
	w := contextGet(t, fw, cfg, demoAdmin, url.Values{"ref": {ref}, "format": {"markdown"}})
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") {
		t.Fatalf("status %d, type %q", w.Code, w.Header().Get("Content-Type"))
	}
	doc := w.Body.String()
	head, block, ok := strings.Cut(doc, bundle.Untrusted)
	if !ok {
		t.Fatalf("no untrusted sentence:\n%s", doc)
	}
	b := decodeBundle(t, contextGet(t, fw, cfg, demoAdmin, url.Values{"ref": {ref}}))
	for _, l := range b.Logs {
		if !strings.Contains(block, l.Msg) {
			t.Errorf("log line %q is not in the block", l.Msg)
		}
	}
	if strings.Contains(head, ref) || (len(b.Logs) > 0 && strings.Contains(head, b.Logs[0].Msg)) {
		t.Errorf("lab data before the sentence:\n%s", head)
	}
}

func TestDemoContextBundleRefusals(t *testing.T) {
	fw, cfg := startDemo(t)
	awaitDemoTopics(t, fw)
	for _, tc := range []struct {
		q    url.Values
		code int
	}{
		{url.Values{"ref": {"not a ref"}}, http.StatusBadRequest},
		{url.Values{}, http.StatusBadRequest},
		{url.Values{"ref": {"kubernetes:Pod/nowhere/nothing"}}, http.StatusNotFound},
		{url.Values{"ref": {"nosuchplugin:Pod/a/b"}}, http.StatusNotFound},
		{url.Values{"ref": {"kubernetes:Secret/media/token"}}, http.StatusNotFound},
		{url.Values{"ref": {"kubernetes:Pod/a/b"}, "format": {"html"}}, http.StatusBadRequest},
		{url.Values{"ref": {"kubernetes:Pod/a/b"}, "include": {"events,secrets"}}, http.StatusBadRequest},
	} {
		if w := contextGet(t, fw, cfg, demoAdmin, tc.q); w.Code != tc.code {
			t.Errorf("%v: status %d, want %d: %s", tc.q, w.Code, tc.code, w.Body)
		}
	}
}

// GET /api/v1/context scrubs every string of the bundle: a secret dash knows that is also lab
// data (here the attention pod's name) is gone from the JSON and the Markdown alike.
func TestDemoContextBundleIsScrubbed(t *testing.T) {
	fw, cfg := startDemo(t)
	awaitDemoTopics(t, fw)
	ref := attentionPod(t, fw)
	_, _, id, _ := model.ParseRef(ref)
	_, pod, _ := strings.Cut(id, "/")
	reg := secret.NewRegistry()
	reg.Add(secret.New(pod, ""))
	h := api.New(api.Deps{Cfg: cfg, Hub: fw.hub, Plugins: fw.sup, Health: fw.sup, Auth: auth.Fixed(demoAdmin), Secrets: reg}).Handler()
	for _, format := range []string{"json", "markdown"} {
		r := httptest.NewRequest(http.MethodGet, cfg.PublicURL+"/api/v1/context?"+url.Values{"ref": {ref}, "format": {format}}.Encode(), nil)
		r.RemoteAddr = "127.0.0.1:41000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), pod) || !strings.Contains(w.Body.String(), "[redacted]") {
			t.Errorf("%s: %d, the pod name %q survives:\n%.600s", format, w.Code, pod, w.Body)
		}
	}
}

// GET /api/v1/plugins says which plugins answer for their objects (objects: true), so the web
// UI offers "Copy context for AI" only for their refs: exactly the instances the context route
// asks.
func TestDemoPluginsSayWhichHaveObjects(t *testing.T) {
	fw, cfg := startDemo(t)
	awaitDemoTopics(t, fw)
	h := api.New(api.Deps{Cfg: cfg, Hub: fw.hub, Plugins: fw.sup, Health: fw.sup, Auth: auth.Fixed(demoAdmin)}).Handler()
	r := httptest.NewRequest(http.MethodGet, cfg.PublicURL+"/api/v1/plugins", nil)
	r.RemoteAddr = "127.0.0.1:41000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var infos []model.PluginInfo
	if err := json.Unmarshal(w.Body.Bytes(), &infos); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	answering := map[string]bool{}
	for _, id := range fw.sup.ObjectInstances() {
		answering[string(id)] = true
	}
	with, without := 0, 0
	for _, pi := range infos {
		if pi.Objects != answering[pi.ID] {
			t.Errorf("%s: objects %v, answers for objects %v", pi.ID, pi.Objects, answering[pi.ID])
		}
		if pi.Objects {
			with++
		} else {
			without++
		}
	}
	byID := map[string]bool{}
	for _, pi := range infos {
		byID[pi.ID] = pi.Objects
	}
	if !byID["kubernetes"] || !byID["alertmanager"] || byID["launcher"] || with == 0 || without == 0 {
		t.Errorf("objects by plugin: %v", byID)
	}
}
