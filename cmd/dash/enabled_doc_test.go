package main

import (
	"bytes"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/pluginconf"
)

// joinWith lists ids as "a, b and c" (last = "and") or "a, b or c".
func joinWith(ids []string, last string) string {
	switch len(ids) {
	case 0:
		return ""
	case 1:
		return ids[0]
	}
	return strings.Join(ids[:len(ids)-1], ", ") + " " + last + " " + ids[len(ids)-1]
}

// squash is s with every run of white space, and the backticks of markdown, gone: a sentence
// wrapped over lines or set in code spans compares equal to the same sentence on one line.
func squash(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ")
}

// The `enabled` doc sentence says exactly which plugins apply a change live and what restarts
// with them, as the manifests say it: every plugin dash ships behaves like that, and each plugin
// that uses or requires another is listed as restarting with it. The sentence is in the
// pluginconf doc tag, in `dash config env` and in the generated page (its configuration intro).
func TestEnabledDocNamesTheUsers(t *testing.T) {
	var live []string
	dependents := map[plugin.ID][]string{}
	for _, m := range builtinManifests() {
		live = append(live, string(m.ID))
		for _, dep := range append(slices.Clone(m.Uses), m.Requires...) {
			dependents[dep] = append(dependents[dep], string(m.ID))
		}
	}
	var pairs []string
	for _, m := range builtinManifests() {
		if users := dependents[m.ID]; len(users) > 0 {
			pairs = append(pairs, "a "+string(m.ID)+" change restarts "+joinWith(users, "and"))
		}
	}
	if len(pairs) == 0 {
		t.Fatalf("setup: no plugin uses or requires another")
	}
	if !slices.Contains(dependents["kubernetes"], "flux") {
		t.Fatalf("setup: flux does not require kubernetes: %v", dependents)
	}
	restarts := "(" + strings.Join(pairs, "; ") + ")"

	field, _ := reflect.TypeFor[pluginconf.PluginBase]().FieldByName("Enabled")
	var table, page bytes.Buffer
	if err := config.WriteEnvTable(&table); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteEnvMarkdown(&page); err != nil {
		t.Fatal(err)
	}

	tagWants := []string{"This is how " + joinWith(live, "and") + " behave", restarts + "."}
	missing := func(text string, wants []string) []string {
		var out []string
		text = squash(text)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				out = append(out, want)
			}
		}
		return out
	}
	for _, doc := range []struct {
		name, text string
		wants      []string
	}{
		{"the enabled doc tag", field.Tag.Get("doc"), tagWants},
		{"dash config env", table.String(), []string{
			"applies live for " + joinWith(live, "and") + ",", restarts + ". A change to any section that is not a plugin's needs a restart, except the administrator lists (auth.proxy.adminUsers, auth.proxy.adminGroups, auth.oidc.adminUsers, auth.oidc.adminGroups and auth.oidc.adminRoles), which apply without one.",
		}},
		{"the generated page", page.String(), []string{
			"applies live for " + joinWith(live, "and") + ",", restarts + ". A change to any section that is not a plugin's needs a restart, except the administrator lists (auth.proxy.adminUsers, auth.proxy.adminGroups, auth.oidc.adminUsers, auth.oidc.adminGroups and auth.oidc.adminRoles), which apply without one.",
		}},
	} {
		for _, want := range missing(doc.text, doc.wants) {
			t.Errorf("%s lacks %q", doc.name, want)
		}
	}

	// The check itself: each stale form of the sentence is caught.
	tag := squash(field.Tag.Get("doc"))
	for name, stale := range map[string]string{
		"a restart pair is missing":     strings.Replace(tag, " a gatus change restarts hardware and kubernetes;", "", 1),
		"flux is missing from the pair": strings.Replace(tag, "restarts proxmox, flux and cnpg", "restarts proxmox and cnpg", 1),
		"cnpg is missing from the pair": strings.Replace(tag, "hardware, kubernetes, cnpg and rustfs", "hardware, kubernetes and rustfs", 1),
		"a live plugin is missing":      strings.Replace(tag, "kubernetes, flux, cnpg, rustfs, grafana", "kubernetes, flux, cnpg, grafana", 1),
	} {
		if stale == tag {
			t.Fatalf("%s: the doc has no text to damage", name)
		}
		if len(missing(stale, tagWants)) == 0 {
			t.Errorf("%s: the check still passes", name)
		}
	}
}

// rustfs is the one plugin that is off until plugins.rustfs.enabled is true, so the shared
// `enabled` sentence ("Unset turns it on when it has the settings it needs") carries that
// exception in each of its copies, and the plugin behaves as the sentence says.
func TestEnabledDocStatesTheRustFSException(t *testing.T) {
	const want = "rustfs is off unless enabled: true"

	field, _ := reflect.TypeFor[pluginconf.PluginBase]().FieldByName("Enabled")
	var table, page bytes.Buffer
	if err := config.WriteEnvTable(&table); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteEnvMarkdown(&page); err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../../internal/config/testdata/config.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile("../../docs/configuration.md")
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile("../../docs/plugins/README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, doc := range []struct{ name, text string }{
		{"the enabled doc tag", field.Tag.Get("doc")},
		{"dash config env", table.String()},
		{"the generated page", page.String()},
		{"docs/configuration.md", string(generated)},
		{"the schema golden", string(schema)},
		{"docs/plugins/README.md", string(index)},
	} {
		if !strings.Contains(squash(doc.text), want) {
			t.Errorf("%s lacks %q", doc.name, want)
		}
	}

	// The sentence is true: rustfs is configured with enabled: true and only then.
	var rustfsManifest *plugin.Manifest
	for _, m := range builtinManifests() {
		if m.ID == "rustfs" {
			rustfsManifest = &m
		}
	}
	if rustfsManifest == nil {
		t.Fatal("rustfs is not a builtin plugin")
	}
	rustfs, ok := builtinPlugin("rustfs")
	if !ok {
		t.Fatal("rustfs is not a builtin plugin")
	}
	p, ok := rustfs.(plugin.Configured)
	if !ok {
		t.Fatal("the rustfs plugin does not say whether it is configured")
	}
	for _, tc := range []struct {
		name    string
		enabled *bool
		want    bool
	}{{"unset", nil, false}, {"false", new(false), false}, {"true", new(true), true}} {
		cfg := rustfsManifest.Config().(*pluginconf.RustFS)
		cfg.Enabled = tc.enabled
		if got := p.Configured(cfg); got != tc.want {
			t.Errorf("rustfs with enabled %s is configured = %v, want %v", tc.name, got, tc.want)
		}
	}
}
