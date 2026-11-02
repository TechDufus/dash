package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/secret"
)

const canary = "CANARYSECRET123456"

var pveBase = []string{"DASH_DEMO=true", "DASH_PLUGINS_PROXMOX_URL=https://pve.example.com:8006", "DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t"}

func resolveWith(t *testing.T, src Sources) (*Resolved, string, error) {
	t.Helper()
	log, buf := logBuf()
	src.Log = log
	res, err := Resolve(src)
	return res, buf.String(), err
}

func writeSecretFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A resolved config never shows a secret through fmt, slog or json (P1.4 acceptance).
func TestResolvedConfigNeverContainsTheSecret(t *testing.T) {
	reg := secret.NewRegistry()
	res, _, err := resolveWith(t, Sources{
		Environ:  append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=" + canary}, pveBase...),
		Registry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tree.Plugins.Proxmox.TokenSecret.Reveal() != canary {
		t.Fatal("the secret must be usable through Reveal")
	}

	var logged strings.Builder
	slog.New(slog.NewJSONHandler(&logged, nil)).Info("cfg", "config", res.Config, "tree", res.Tree, "token", res.Tree.Plugins.Proxmox.TokenSecret)
	asJSON := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for name, out := range map[string]string{
		"%v":        fmt.Sprintf("%v %v", res.Config, res.Tree),
		"%+v":       fmt.Sprintf("%+v %+v", res.Config, res.Tree),
		"%#v":       fmt.Sprintf("%#v %#v", res.Config, res.Tree),
		"slog json": logged.String(),
		"json":      asJSON(res.Config) + asJSON(res.Tree) + asJSON(res.Provenance),
	} {
		if strings.Contains(out, canary) {
			t.Errorf("%s output contains the secret", name)
		}
	}
	if !strings.Contains(asJSON(res.Tree.Plugins.Proxmox), `"tokenSecret":{"set":true}`) {
		t.Errorf("the tree shows secret metadata only: %s", asJSON(res.Tree.Plugins.Proxmox))
	}

	// Every form of the resolved secret is known to the registry (P1.4: redaction).
	for _, form := range []string{
		canary,
		base64.StdEncoding.EncodeToString([]byte(canary)),
		base64.RawURLEncoding.EncodeToString([]byte(canary)),
	} {
		if got := reg.Scrub("upstream said: " + form); strings.Contains(got, form) {
			t.Errorf("registry did not scrub %q: %q", form, got)
		}
	}
}

func TestSecretLayersAcceptLiteralEnvAndFileRefs(t *testing.T) {
	file := writeSecretFile(t, "  "+canary+"\n")
	t.Setenv("PVE_TOKEN_SECRET", canary) // env: references read the process environment
	tests := []struct {
		name    string
		src     Sources
		wantRef string
		prov    Provenance
	}{
		{"env literal", Sources{Environ: append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=" + canary}, pveBase...)}, "", Provenance{SourceEnv, "DASH_PLUGINS_PROXMOX_TOKEN_SECRET"}},
		{"env: reference", Sources{Environ: append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=env:PVE_TOKEN_SECRET", "PVE_TOKEN_SECRET=" + canary}, pveBase...)}, "env:PVE_TOKEN_SECRET", Provenance{SourceEnv, "DASH_PLUGINS_PROXMOX_TOKEN_SECRET"}},
		{"file: reference", Sources{Environ: append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=file:" + file}, pveBase...)}, "file:" + file, Provenance{SourceEnv, "DASH_PLUGINS_PROXMOX_TOKEN_SECRET"}},
		{"_FILE shorthand is a file: reference", Sources{Environ: append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE=" + file}, pveBase...)}, "file:" + file, Provenance{SourceEnv, "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE"}},
		{"file layer file: reference", Sources{
			Environ: pveBase[:2],
			File:    &File{Path: "c.yaml", Data: []byte("plugins: {proxmox: {tokenID: 'u@pve!t', tokenSecret: 'file:" + file + "'}}\n")},
		}, "file:" + file, Provenance{SourceFile, "c.yaml"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, log, err := resolveWith(t, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			v := res.Tree.Plugins.Proxmox.TokenSecret
			if v.Reveal() != canary || v.Ref() != tc.wantRef {
				t.Errorf("secret ref %q (value ok: %v), want ref %q", v.Ref(), v.Reveal() == canary, tc.wantRef)
			}
			if got := res.Provenance["plugins.proxmox.tokenSecret"]; got != tc.prov {
				t.Errorf("provenance = %+v, want %+v", got, tc.prov)
			}
			if strings.Contains(log, canary) || strings.Contains(log, "level=WARN") {
				t.Errorf("unexpected log:\n%s", log)
			}
		})
	}
}

// An unreadable reference fails startup naming the field and the layer, never the value.
func TestUnreadableSecretReferenceFailsNamingFieldNotValue(t *testing.T) {
	empty := writeSecretFile(t, " \n")
	tests := []struct {
		name string
		env  string
		want []string
	}{
		{"missing file", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE=/missing/secret", []string{"plugins.proxmox.tokenSecret", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE", "/missing/secret"}},
		{"missing file: ref", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET=file:/missing/secret", []string{"plugins.proxmox.tokenSecret", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET", "/missing/secret"}},
		{"empty file", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET_FILE=" + empty, []string{"plugins.proxmox.tokenSecret", "is empty"}},
		{"unset variable", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET=env:DASH_TEST_NOT_SET", []string{"plugins.proxmox.tokenSecret", "DASH_TEST_NOT_SET is not set"}},
		{"k8s: needs the plugin", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET=k8s:dash/proxmox#" + canary, []string{"plugins.proxmox.tokenSecret", "kubernetes plugin"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveWith(t, Sources{Environ: append([]string{tc.env}, pveBase...)})
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
			if strings.Contains(err.Error(), canary) {
				t.Errorf("error contains the value: %v", err)
			}
		})
	}
}

func TestLiteralSecretInTheConfigFileWarnsOnce(t *testing.T) {
	file := &File{Path: "/etc/dash/config.yaml", Data: []byte("plugins: {proxmox: {url: 'https://pve.example.com:8006', tokenID: 'u@pve!t', tokenSecret: " + canary + "}}\n")}
	res, log, err := resolveWith(t, Sources{Environ: []string{"DASH_DEMO=true"}, File: file})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tree.Plugins.Proxmox.TokenSecret.Reveal() != canary {
		t.Error("a literal in the file still works")
	}
	if n := strings.Count(log, "level=WARN"); n != 1 || !strings.Contains(log, "plugins.proxmox.tokenSecret") || !strings.Contains(log, "/etc/dash/config.yaml") {
		t.Errorf("want one warning naming the field and file, got:\n%s", log)
	}
	if strings.Contains(log, canary) {
		t.Errorf("the warning contains the value:\n%s", log)
	}
	// A literal in the environment is how most deployments pass it: no warning.
	_, log, err = resolveWith(t, Sources{Environ: append([]string{"DASH_PLUGINS_PROXMOX_TOKEN_SECRET=" + canary}, pveBase...)})
	if err != nil || strings.Contains(log, "WARN") {
		t.Errorf("env literal: err %v, log %q", err, log)
	}
}

// The UI layer cannot point a secret at an env variable or file of the dash process (S.14), nor
// store one in clear; the layers above it still decide.
func TestUILayerCannotSetASecretFromEnvFileOrLiteral(t *testing.T) {
	file := writeSecretFile(t, canary)
	for _, raw := range []string{"env:HOME", "file:" + file, "plain-" + canary} {
		t.Run(strings.SplitN(raw, ":", 2)[0], func(t *testing.T) {
			ui := map[string]any{"plugins": map[string]any{"proxmox": map[string]any{"tokenID": "u@pve!t", "tokenSecret": raw}}}
			_, log, err := resolveWith(t, Sources{Environ: append([]string{}, pveBase...), UI: ui})
			if err == nil || !strings.Contains(err.Error(), "tokenID and tokenSecret must be set together") {
				t.Fatalf("the UI secret must be ignored, leaving the token incomplete: %v", err)
			}
			if !strings.Contains(log, "path=plugins.proxmox.tokenSecret") || strings.Contains(log, canary) {
				t.Errorf("want a warning naming the path and not the value:\n%s", log)
			}
		})
	}
	// A k8s: reference is accepted from the UI; it errors until the kubernetes plugin resolves it.
	ui := map[string]any{"plugins": map[string]any{"proxmox": map[string]any{"tokenSecret": "k8s:dash/proxmox#token"}}}
	_, _, err := resolveWith(t, Sources{Environ: pveBase, UI: ui})
	if err == nil || !strings.Contains(err.Error(), "plugins.proxmox.tokenSecret (UI settings)") || !strings.Contains(err.Error(), "kubernetes plugin") {
		t.Errorf("k8s: from the UI: %v", err)
	}
}
