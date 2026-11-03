package config

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/secret"
	"github.com/TechDufus/dash/internal/settings"
)

// storeRig is a running configuration from a config file, the environment and a settings
// store, whose k8s: references go through one K8sRefs, with a Reloader on top, as cmd/dash
// wires them.
type storeRig struct {
	env     []string
	cfgPath string
	store   *settings.Store
	refs    *secret.K8sRefs
	res     *Resolved
	rl      *Reloader
	log     *bytes.Buffer
	changes []Change
}

// newStoreRig starts from cfg in the config file and stored (dotted paths) in the store.
func newStoreRig(t *testing.T, cfg string, stored map[string]any) *storeRig {
	t.Helper()
	dir := t.TempDir()
	reg := secret.NewRegistry()
	g := &storeRig{cfgPath: filepath.Join(dir, "config.yaml"), refs: secret.NewK8sRefs(reg)}
	writeFile(t, g.cfgPath, cfg)
	st, err := settings.Open(dir, settings.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	g.store = st
	if _, err := st.Apply(stored, nil, nil); err != nil {
		t.Fatal(err)
	}
	g.env = []string{"DASH_DEMO=true", "DASH_CONFIG=" + g.cfgPath, "DASH_DATA_DIR=" + dir}
	f, err := readConfigFile(g.env)
	if err != nil {
		t.Fatal(err)
	}
	log, buf := logBuf()
	g.log = buf
	g.res, err = Resolve(Sources{Environ: g.env, File: f, UI: st.Values(), UIRef: st.Path(), Registry: reg, Log: log, K8s: g.refs})
	if err != nil {
		t.Fatal(err)
	}
	g.rl = NewReloader(g.res, st, ReloadOptions{Log: log})
	g.rl.Subscribe(func(c Change) { g.changes = append(g.changes, c) })
	return g
}

func TestCandidateDoesNotMutateTheRunningConfig(t *testing.T) {
	g := newStoreRig(t, headlampConfig("https://hl-a.example.com"), map[string]any{"plugins.grafana.url": "https://gf-a.example.com"})
	running, tree, rev, stored, refs := g.rl.Resolved(), g.rl.Tree(), g.store.Revision(), g.store.Values(), g.refs.Refs()

	cand, err := g.rl.Candidate(map[string]any{"plugins": map[string]any{
		"grafana": map[string]any{"url": "https://gf-b.example.com"},
		"proxmox": proxmoxUI(refA), // a reference nothing has used yet
	}})
	if err != nil {
		t.Fatal(err)
	}

	// The candidate is the configuration as it would be.
	if cand.Tree.Plugins.Grafana.URL != "https://gf-b.example.com" {
		t.Errorf("candidate grafana url = %q", cand.Tree.Plugins.Grafana.URL)
	}
	if p := cand.Provenance["plugins.grafana.url"]; p != (Provenance{Source: SourceUI, Ref: g.store.Path()}) {
		t.Errorf("candidate provenance = %+v", p)
	}
	if v := cand.Tree.Plugins.Proxmox.TokenSecret; v.Ref() != refA || !v.Pending() {
		t.Errorf("candidate token: ref %q pending %v", v.Ref(), v.Pending())
	}
	if cand.Tree.Plugins.Headlamp.URL != "https://hl-a.example.com" {
		t.Errorf("the candidate lost the config file: %q", cand.Tree.Plugins.Headlamp.URL)
	}

	// Nothing that runs has moved.
	if g.rl.Resolved() != running || !reflect.DeepEqual(g.rl.Tree(), tree) {
		t.Error("Candidate replaced the running resolution")
	}
	if got := g.rl.Tree().Plugins.Grafana.URL; got != "https://gf-a.example.com" {
		t.Errorf("running grafana url = %q", got)
	}
	if g.store.Revision() != rev || !reflect.DeepEqual(g.store.Values(), stored) {
		t.Error("Candidate wrote to the store")
	}
	if got := g.refs.Refs(); !reflect.DeepEqual(got, refs) {
		t.Errorf("Candidate handed out a k8s: handle, which the kubernetes plugin would watch: %v", got)
	}
	if len(g.changes) != 0 || len(g.rl.RestartRequired()) != 0 {
		t.Errorf("Candidate announced changes %+v, restart %v", g.changes, g.rl.RestartRequired())
	}

	// The next tick finds nothing to do: the candidate consumed no change.
	g.rl.poll()
	if len(g.changes) != 0 || g.rl.Tree().Plugins.Grafana.URL != "https://gf-a.example.com" {
		t.Errorf("after a tick: changes %+v, url %q", g.changes, g.rl.Tree().Plugins.Grafana.URL)
	}
}

func TestCandidateReportsPathQualifiedErrors(t *testing.T) {
	// The store holds a key of another version; startup only warned about it.
	g := newStoreRig(t, headlampConfig("https://hl-a.example.com"), map[string]any{"plugins.grafana.urll": "stale"})
	if !strings.Contains(g.log.String(), "plugins.grafana.urll") {
		t.Fatalf("startup did not warn about the stale key:\n%s", g.log)
	}

	ui := map[string]any{
		"plugins": map[string]any{
			"grafana":  map[string]any{"urll": "stale", "url": 5},
			"headlamp": map[string]any{"urlx": "https://hl-b.example.com"},
		},
		"server": map[string]any{"listen": "0.0.0.0:9999"},
	}
	_, err := g.rl.Candidate(ui)
	if err == nil {
		t.Fatal("a candidate with invalid UI values resolved")
	}
	want := []FieldError{
		{Path: "plugins.grafana.url", Message: "expected a string, got number"},
		{Path: "plugins.headlamp.urlx", Message: "unknown field"},
		{Path: "server.listen", Message: "server, auth, settings.ui and dataDir cannot be set from the UI"},
	}
	if got := FieldErrors(err); !reflect.DeepEqual(got, want) {
		t.Errorf("FieldErrors = %+v\nwant one per changed path: %+v", got, want)
	}
	// The text is one "path: message" line each.
	var lines []string
	for _, fe := range want {
		lines = append(lines, fe.Path+": "+fe.Message)
	}
	if err.Error() != strings.Join(lines, "\n") {
		t.Errorf("error text:\n%s", err)
	}

	// A key the store already holds stays a warning, or a store with keys of another version
	// would refuse every write.
	if _, err := g.rl.Candidate(g.store.Values()); err != nil {
		t.Errorf("the stored values alone: %v", err)
	}
	// A secret error names the field, never the reference; its text keeps the layer.
	_, err = g.rl.Candidate(map[string]any{"plugins": map[string]any{"proxmox": proxmoxUI("k8s:Not_A_Namespace/x#y")}})
	fes := FieldErrors(err)
	if len(fes) != 1 || fes[0].Path != tokenPath || fes[0].Message == "" || strings.Contains(err.Error(), "Not_A_Namespace") {
		t.Fatalf("malformed reference: %v, FieldErrors %+v", err, fes)
	}
	if want := tokenPath + " (UI settings): " + fes[0].Message; err.Error() != want {
		t.Errorf("error text = %q, want %q", err, want)
	}

	// A cross-field rule of validate sits at its field too: the token pair half set.
	_, err = g.rl.Candidate(map[string]any{"plugins": map[string]any{"proxmox": map[string]any{
		"url": "https://pve.example.com:8006", "tokenID": "u@pve!t",
	}}})
	wantPair := []FieldError{{Path: tokenPath, Message: "tokenID and tokenSecret must be set together"}}
	if got := FieldErrors(err); !reflect.DeepEqual(got, wantPair) {
		t.Errorf("token pair: FieldErrors = %+v (error %v), want %+v", got, err, wantPair)
	}
	if err == nil || err.Error() != tokenPath+": tokenID and tokenSecret must be set together" {
		t.Errorf("token pair error text = %v", err)
	}

	// Resolve without the strict mode, as at startup and on a reload, still only warns.
	f, ferr := readConfigFile(g.env)
	if ferr != nil {
		t.Fatal(ferr)
	}
	log, buf := logBuf()
	if _, err := Resolve(Sources{Environ: g.env, File: f, UI: ui, UIRef: g.store.Path(), Log: log}); err != nil {
		t.Errorf("Resolve refused UI values it must ignore: %v", err)
	}
	for _, p := range []string{"plugins.grafana.url", "plugins.headlamp.urlx", "server.listen"} {
		if !strings.Contains(buf.String(), "path="+p) {
			t.Errorf("Resolve did not warn about %s:\n%s", p, buf)
		}
	}
}

// proxmoxUI is a complete Proxmox section as the UI would send it, with the token secret ref.
func proxmoxUI(ref string) map[string]any {
	return map[string]any{"url": "https://pve.example.com:8006", "tokenID": "u@pve!t", "tokenSecret": ref}
}
