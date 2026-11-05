package config

import (
	"reflect"
	"slices"
	"testing"
	"unsafe"

	"github.com/TechDufus/dash/internal/pluginconf"
	"github.com/TechDufus/dash/internal/secret"
)

func TestFlattenPatch(t *testing.T) {
	stored := map[string]any{"plugins": map[string]any{"proxmox": map[string]any{
		"node": "pve", "storage": map[string]any{"source": "prometheus"}, "criticalVMs": map[string]any{"ids": []any{1.0}},
	}}}
	p, errs := FlattenPatch("plugins.proxmox", map[string]any{
		"url": "https://pve.example.com", "node": nil, "storage": nil,
		"criticalVMs": map[string]any{"tags": []any{"a"}}, "nope": 1, "tls": "x",
	}, stored)
	wantSets := map[string]any{"plugins.proxmox.url": "https://pve.example.com", "plugins.proxmox.criticalVMs.tags": []any{"a"}}
	if !reflect.DeepEqual(p.Sets, wantSets) {
		t.Errorf("sets = %v", p.Sets)
	}
	if want := []string{"plugins.proxmox.node", "plugins.proxmox.storage.source"}; !slices.Equal(p.Deletes, want) {
		t.Errorf("deletes = %v, want %v (a null section removes what is stored under it, nothing else)", p.Deletes, want)
	}
	wantErrs := []FieldError{{Path: "plugins.proxmox.nope", Message: "unknown field"}, {Path: "plugins.proxmox.tls", Message: "expected a section (a map of settings)"}}
	if !reflect.DeepEqual(errs, wantErrs) {
		t.Errorf("errs = %v", errs)
	}
	if got := p.Paths(); !slices.Equal(got, []string{"plugins.proxmox.criticalVMs.tags", "plugins.proxmox.node", "plugins.proxmox.storage.source", "plugins.proxmox.url"}) {
		t.Errorf("paths = %v", got)
	}
	if _, errs := FlattenPatch("plugins.proxmox.url", nil, nil); len(errs) != 1 {
		t.Error("a leaf is no section to patch")
	}
}

// A patch's paths are the schema's own strings, not built per request: the audit records that
// keep them share one copy, which the rings' 1 MiB bound counts on
// (audit.TestRingsOfSettingsWritesStayUnderOneMiB). Sets, leaf deletes and section deletes alike,
// from a plugin section and from the root.
func TestFlattenPatchPathsAreTheSchemasStrings(t *testing.T) {
	stored := map[string]any{"plugins": map[string]any{"proxmox": map[string]any{"storage": map[string]any{"source": "prometheus"}}}}
	plugin, _ := FlattenPatch("plugins.proxmox", map[string]any{
		"url": "https://pve.example.com", "node": nil, "storage": nil, "criticalVMs": map[string]any{"tags": []any{"a"}},
	}, stored)
	root, _ := FlattenPatch("", map[string]any{"ui": map[string]any{"iconBaseURL": "https://icons.example.com/"}}, nil)
	var paths []string
	for _, p := range []Patch{plugin, root} {
		for k := range p.Sets {
			paths = append(paths, k)
		}
		paths = append(paths, p.Deletes...)
	}
	if len(paths) != 5 {
		t.Fatalf("paths = %v", paths)
	}
	for _, path := range paths {
		l := schema.byPath[path]
		if l == nil {
			t.Fatalf("no leaf %s", path)
		}
		if unsafe.StringData(path) != unsafe.StringData(l.path) {
			t.Errorf("%s: a string of its own, not the schema's", path)
		}
	}
}

func TestUILayerAppliesSetsAndPrunesDeletes(t *testing.T) {
	stored := map[string]any{"plugins": map[string]any{"proxmox": map[string]any{"node": "pve"}, "gatus": map[string]any{"url": "u"}}}
	got := UILayer(stored, map[string]any{"plugins.grafana.url": "g"}, []string{"plugins.proxmox.node", "plugins.absent.x"})
	want := map[string]any{"plugins": map[string]any{"gatus": map[string]any{"url": "u"}, "grafana": map[string]any{"url": "g"}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("layer = %v", got)
	}
	if _, ok := StoredAt(stored, "plugins.proxmox.node"); !ok {
		t.Error("UILayer modified the store's map")
	}
	if got := UILayer(nil, nil, nil); got == nil || len(got) != 0 {
		t.Errorf("empty layer = %v", got)
	}
}

func TestHasReference(t *testing.T) {
	for v, want := range map[string]bool{"env:X": true, " file:/x": true, "k8s:a/b#c": false, "https://x": false, "xenv:": false} {
		if HasReference(v) != want {
			t.Errorf("%q: %v", v, !want)
		}
	}
	if !HasReference([]any{"a", map[string]any{"b": "file:/etc/x"}}) {
		t.Error("a reference inside a list of objects")
	}
}

// DestinationChanged is a new host or port, or any new dest value; DestinationEdited is any new
// value of a url or dest leaf, a new path or scheme or another spelling of the host included.
func TestDestinationChanged(t *testing.T) {
	at := func(mod func(*Tree)) *Resolved {
		r := &Resolved{Tree: DefaultTree()}
		r.Tree.Plugins.Proxmox.URL = "https://pve.example.com"
		if mod != nil {
			mod(&r.Tree)
		}
		return r
	}
	cur := at(nil)
	for name, tc := range map[string]struct {
		mod          func(*Tree)
		want, edited bool
	}{
		"same":             {nil, false, false},
		"path":             {func(t *Tree) { t.Plugins.Proxmox.URL = "https://pve.example.com/api" }, false, true},
		"explicit default": {func(t *Tree) { t.Plugins.Proxmox.URL = "https://PVE.example.com:443" }, false, true},
		"scheme only":      {func(t *Tree) { t.Plugins.Proxmox.URL = "http://pve.example.com:443" }, true, true},
		"host":             {func(t *Tree) { t.Plugins.Proxmox.URL = "https://other.example.com" }, true, true},
		"port":             {func(t *Tree) { t.Plugins.Proxmox.URL = "https://pve.example.com:8006" }, true, true},
		"scheme port":      {func(t *Tree) { t.Plugins.Proxmox.URL = "http://pve.example.com" }, true, true},
		"first set":        {func(t *Tree) {}, false, false},
		"caFile":           {func(t *Tree) { t.Plugins.Proxmox.TLS.CAFile = "/ca.pem" }, true, true},
		"insecure":         {func(t *Tree) { t.Plugins.Proxmox.TLS.InsecureSkipVerify = true }, true, true},
		"other plugin":     {func(t *Tree) { t.Plugins.Gatus.URL = "https://g.example.com" }, false, false},
		"other leaf":       {func(t *Tree) { t.Plugins.Proxmox.Node = "pve2" }, false, false},
	} {
		next := at(tc.mod)
		if got := DestinationChanged(cur, next, "plugins.proxmox"); got != tc.want {
			t.Errorf("%s: changed %v", name, got)
		}
		if got := DestinationEdited(cur, next, "plugins.proxmox"); got != tc.edited {
			t.Errorf("%s: edited %v", name, got)
		}
	}
	empty := &Resolved{Tree: DefaultTree()}
	if !DestinationChanged(empty, cur, "plugins.proxmox") {
		t.Error("a URL set for the first time is a new destination")
	}
}

// A secret replaced in a candidate's copy leaves the handle the running configuration shares.
func TestReplaceSecretInLeavesTheSharedHandle(t *testing.T) {
	running := secret.New("running-secret", "")
	tree := DefaultTree()
	tree.Plugins.Gatus.Auth.BearerToken = running
	var cfg any
	for _, s := range tree.PluginSections() {
		if s.Name == "gatus" {
			cfg = s.Value
		}
	}
	if err := ReplaceSecretIn(cfg, "plugins.gatus.auth.bearerToken", secret.Value{}); err != nil {
		t.Fatal(err)
	}
	if cfg.(*pluginconf.Gatus).Auth.BearerToken.IsSet() || running.Reveal() != "running-secret" {
		t.Error("the copy kept the secret, or the shared handle changed")
	}
	if err := ReplaceSecretIn(cfg, "plugins.proxmox.tokenSecret", secret.Value{}); err == nil {
		t.Error("a secret of another section was accepted")
	}
	if err := ReplaceSecretIn(cfg, "plugins.gatus.url", secret.Value{}); err == nil {
		t.Error("a leaf that is no secret was accepted")
	}
}
