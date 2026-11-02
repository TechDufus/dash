package config

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/secret"
)

const (
	refA = "k8s:dash/pve-token#secret"
	refB = "k8s:dash/other-token#secret"
)

func proxmoxWith(ref string) string {
	return headlampConfig("https://hl-a.example.com") + "  proxmox:\n    url: https://pve.example.com:8006\n    tokenID: u@pve!t\n    tokenSecret: '" + ref + "'\n"
}

// k8sRig is a running configuration whose k8s: references go through one K8sRefs, with a
// Reloader on top, as cmd/dash wires them.
type k8sRig struct {
	cfgPath string
	refs    *secret.K8sRefs
	reg     *secret.Registry
	res     *Resolved
	rl      *Reloader
	log     fmt.Stringer
	changes []Change
	reloads int
}

func newK8sRig(t *testing.T, cfg string) *k8sRig {
	t.Helper()
	g := &k8sRig{reg: secret.NewRegistry()}
	g.refs = secret.NewK8sRefs(g.reg)
	g.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, g.cfgPath, cfg)
	env := []string{"DASH_DEMO=true", "DASH_CONFIG=" + g.cfgPath}
	f, err := readConfigFile(env)
	if err != nil {
		t.Fatal(err)
	}
	log, buf := logBuf()
	g.log = buf
	g.res, err = Resolve(Sources{Environ: env, File: f, Registry: g.reg, Log: log, K8s: g.refs})
	if err != nil {
		t.Fatal(err)
	}
	g.rl = NewReloader(g.res, nil, ReloadOptions{Log: log})
	g.rl.Subscribe(func(c Change) { g.changes = append(g.changes, c) })
	g.refs.OnChange(func() { g.reloads++ })
	return g
}

func (g *k8sRig) edit(t *testing.T, cfg string) {
	t.Helper()
	writeFile(t, g.cfgPath, cfg)
	g.changes = nil
	g.rl.poll()
}

// A well-formed reference loads as a pending handle, in the file, the environment and the UI
// layer alike; a pending secret counts as given, so the Proxmox rule "tokenID and tokenSecret
// together" holds (TestPendingHandleValidates).
func TestPendingHandleValidates(t *testing.T) {
	refs := secret.NewK8sRefs(nil)
	env := append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=" + refA}, pveBase...)
	res, log, err := resolveWith(t, Sources{Environ: env, K8s: refs})
	if err != nil {
		t.Fatalf("a pending handle must pass the load: %v", err)
	}
	v := res.Tree.Plugins.Proxmox.TokenSecret
	if !v.Pending() || v.IsSet() || v.Ref() != refA {
		t.Errorf("pending=%v set=%v ref=%q", v.Pending(), v.IsSet(), v.Ref())
	}
	if p := res.Provenance[tokenPath]; p.Source != SourceEnv {
		t.Errorf("provenance = %+v", p)
	}
	if strings.Contains(log, "level=WARN") {
		t.Errorf("a reference in the environment is no warning:\n%s", log)
	}
	// Without the tokenID the pair is still incomplete: the rule is not switched off.
	_, _, err = resolveWith(t, Sources{Environ: []string{"DASH_DEMO=true", "DASH_PLUGINS_PROXMOX_URL=https://pve.example.com:8006", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET=" + refA}, K8s: refs})
	if err == nil || !strings.Contains(err.Error(), "tokenID and tokenSecret must be set together") {
		t.Errorf("a pending secret without a tokenID: %v", err)
	}
}

// A malformed reference fails the load naming the field and the layer, never the reference.
func TestMalformedK8sRefFailsNamingTheField(t *testing.T) {
	for _, raw := range []string{"k8s:dash/" + canary, "k8s:Dash/x#" + canary, "k8s:dash/x_y#" + canary} {
		_, _, err := resolveWith(t, Sources{
			Environ: append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=" + raw}, pveBase...),
			K8s:     secret.NewK8sRefs(nil),
		})
		if err == nil || !strings.Contains(err.Error(), tokenPath) || !strings.Contains(err.Error(), "DASH_PLUGINS_PROXMOX_TOKEN_SECRET") {
			t.Errorf("%q: error = %v", raw, err)
		}
		if err != nil && strings.Contains(err.Error(), canary) {
			t.Errorf("the error repeats the reference: %v", err)
		}
	}
}

// SkipSecrets (dash config validate) checks the syntax of a reference and reads nothing.
func TestSkipSecretsChecksK8sSyntaxOnly(t *testing.T) {
	good := append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=" + refA}, pveBase...)
	if _, _, err := resolveWith(t, Sources{Environ: good, K8s: secret.NewK8sRefs(nil), SkipSecrets: true}); err != nil {
		t.Errorf("a well-formed reference: %v", err)
	}
	bad := append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=k8s:no-key"}, pveBase...)
	_, _, err := resolveWith(t, Sources{Environ: bad, K8s: secret.NewK8sRefs(nil), SkipSecrets: true})
	if err == nil || !strings.Contains(err.Error(), tokenPath) {
		t.Errorf("a malformed reference: %v", err)
	}
}

// Every resolution hands the supervisor the same holder, so a filled reference is no change:
// DeepEqual holds between the applied and the reloaded configuration (no restart).
func TestK8sRefsStableHolderAcrossResolveAndReload(t *testing.T) {
	g := newK8sRig(t, proxmoxWith(refA))
	first := g.res.Tree.Plugins.Proxmox.TokenSecret
	if !first.Pending() {
		t.Fatal("the reference must load pending")
	}
	g.refs.Fill(refA, canary)

	g.edit(t, proxmoxWith(refA)+"  # edited\n")
	if len(g.changes) != 0 {
		t.Errorf("an unchanged secret is no change: %+v", g.changes)
	}
	if r := g.rl.RestartRequired(); len(r) != 0 {
		t.Errorf("restart required = %v", r)
	}
	if now := g.rl.Tree().Plugins.Proxmox.TokenSecret; now.Reveal() != canary || !reflect.DeepEqual(now, first) || !reflect.DeepEqual(g.res.Tree, g.rl.Tree()) {
		t.Error("the reloaded configuration must hold the very handle that was filled")
	}

	// A fresh resolution of the same source gives the same handle as well.
	env := []string{"DASH_DEMO=true", "DASH_CONFIG=" + g.cfgPath}
	f, err := readConfigFile(env)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Resolve(Sources{Environ: env, File: f, Log: slog.New(slog.DiscardHandler), K8s: g.refs})
	if err != nil || !reflect.DeepEqual(again.Tree, g.res.Tree) {
		t.Errorf("a second resolution differs from the first: %v", err)
	}
}

// A reload that introduces a reference is a Change of its section, though a pending handle
// holds "" before and after (TestReloadAddingARefIsAChange); the registry already scrubs it.
func TestReloadAddingARefIsAChange(t *testing.T) {
	g := newK8sRig(t, headlampConfig("https://hl-a.example.com"))
	before := g.reloads

	g.edit(t, proxmoxWith(refA))
	if len(g.changes) != 1 || g.changes[0].Section != "plugins.proxmox" || !slices.Contains(g.changes[0].Paths, tokenPath) {
		t.Fatalf("changes = %+v, want plugins.proxmox with %s", g.changes, tokenPath)
	}
	if g.reloads == before {
		t.Error("a reference seen for the first time must trigger the supervisor's Reload")
	}
	if r := g.rl.RestartRequired(); !slices.Contains(r, tokenPath) {
		t.Errorf("the running config has no such secret: restart required = %v", r)
	}
	if g.res.Tree.Plugins.Proxmox.TokenSecret.Present() {
		t.Error("the running config must not gain a secret")
	}
	if refs := g.refs.Refs(); len(refs) != 1 || refs[0].Raw != refA {
		t.Errorf("refs = %+v", refs)
	}

	// Pointing the field at another reference is a change, and never copies a value around.
	g.refs.Fill(refA, canary)
	g.edit(t, proxmoxWith(refB))
	if len(g.changes) != 1 || !slices.Contains(g.changes[0].Paths, tokenPath) {
		t.Fatalf("changes after the second reference = %+v", g.changes)
	}
	a, _ := g.refs.Handle(refA)
	b, _ := g.refs.Handle(refB)
	if a.Reveal() != canary || b.IsSet() {
		t.Errorf("each handle keeps its own value: A=%v B=%v", a.IsSet(), b.IsSet())
	}

	// And back: still a change, still the same two holders.
	g.edit(t, proxmoxWith(refA))
	if len(g.changes) != 1 {
		t.Errorf("changes back to the first reference = %+v", g.changes)
	}
}

// Pointing a field at another handle that already holds the same value is a Change: the equal
// values must not hide that the field reads another Secret.
func TestRepointingAtAnEqualFilledHandleIsAChange(t *testing.T) {
	g := newK8sRig(t, proxmoxWith(refA))
	g.refs.Fill(refA, canary)
	g.edit(t, proxmoxWith(refB))
	g.refs.Fill(refB, canary) // the same value in the other Secret

	g.edit(t, proxmoxWith(refA))
	if len(g.changes) != 1 || g.changes[0].Section != "plugins.proxmox" || !slices.Contains(g.changes[0].Paths, tokenPath) {
		t.Fatalf("changes = %+v, want plugins.proxmox with %s", g.changes, tokenPath)
	}
	g.edit(t, proxmoxWith(refA)+"  # edited\n")
	if len(g.changes) != 0 {
		t.Errorf("the same reference again is no change: %+v", g.changes)
	}
}

// A reload that replaces a running k8s: reference with a literal or a file: reference brings a
// new secret into the tree, which the registry must scrub from then on.
func TestReloadFromK8sRefToAnotherSourceIsScrubbed(t *testing.T) {
	const fresh = "FRESHSECRETVALUE9876"
	secretFile := filepath.Join(t.TempDir(), "token")
	writeFile(t, secretFile, fresh+"\n")
	for name, value := range map[string]string{"literal": fresh, "file": "file:" + secretFile} {
		t.Run(name, func(t *testing.T) {
			g := newK8sRig(t, proxmoxWith(refA))
			g.refs.Fill(refA, canary)
			if got := g.reg.Scrub("said " + fresh); !strings.Contains(got, fresh) {
				t.Fatal("the new value is scrubbed before it was ever configured")
			}

			g.edit(t, proxmoxWith(value))
			if len(g.changes) != 1 || !slices.Contains(g.changes[0].Paths, tokenPath) {
				t.Fatalf("changes = %+v", g.changes)
			}
			if got := g.reg.Scrub("said " + fresh); strings.Contains(got, fresh) {
				t.Errorf("the value a reload brought in is not scrubbed: %q", got)
			}
			if got := g.reg.Scrub("said " + canary); strings.Contains(got, canary) {
				t.Errorf("the value of the k8s: handle stopped being scrubbed: %q", got)
			}
		})
	}
}

// A reference the reload introduced is scrubbed from the moment the Secret is read, and no log
// line, error or formatted configuration ever shows the value (the canary of P3.10).
func TestSecretValueNeverLeaks(t *testing.T) {
	g := newK8sRig(t, headlampConfig("https://hl-a.example.com"))
	g.edit(t, proxmoxWith(refA))
	g.refs.Fill(refA, canary)

	if got := g.reg.Scrub("upstream said " + canary); strings.Contains(got, canary) {
		t.Errorf("a value read through a reload-added reference is not scrubbed: %q", got)
	}
	tree := g.rl.Tree()
	var logged strings.Builder
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("cfg", "tree", tree, "token", tree.Plugins.Proxmox.TokenSecret)
	for name, out := range map[string]string{
		"%v":       fmt.Sprintf("%v %v", tree, g.res.Tree),
		"%+v":      fmt.Sprintf("%+v", tree),
		"%#v":      fmt.Sprintf("%#v", tree.Plugins.Proxmox),
		"slog":     logged.String(),
		"reloader": g.log.String(),
	} {
		if strings.Contains(out, canary) {
			t.Errorf("%s contains the value", name)
		}
	}
	// A rotation lands in the same holder; the old value stays scrubbable.
	g.refs.Fill(refA, canary+"-new")
	if got := g.reg.Scrub(canary + " " + canary + "-new"); strings.Contains(got, canary) {
		t.Errorf("a rotated value or its predecessor is not scrubbed: %q", got)
	}
	if strings.Contains(g.log.String(), canary) {
		t.Errorf("the reloader logged a value:\n%s", g.log)
	}
}
