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
// with them, as the manifests say it: the plugins that run on their own are the ones that behave
// like that, the others are named as not moved yet, and each plugin that uses or requires another
// is listed as restarting with it. A shim never restarts, so it is neither a dependent nor a
// dependency here. The sentence is in the pluginconf doc tag, in `dash config env` (and the
// generated page) and in the README row.
func TestEnabledDocNamesTheUsers(t *testing.T) {
	var live, shims []string
	dependents := map[plugin.ID][]string{}
	for _, m := range builtinManifests() {
		if !hasRealPlugin(m.ID) {
			shims = append(shims, string(m.ID))
			continue
		}
		live = append(live, string(m.ID))
		for _, dep := range append(slices.Clone(m.Uses), m.Requires...) {
			if hasRealPlugin(dep) {
				dependents[dep] = append(dependents[dep], string(m.ID))
			}
		}
	}
	var pairs []string
	for _, m := range builtinManifests() {
		if users := dependents[m.ID]; len(users) > 0 {
			pairs = append(pairs, "a "+string(m.ID)+" change restarts "+joinWith(users, "and"))
		}
	}
	if len(pairs) == 0 || len(shims) == 0 {
		t.Fatalf("setup: pairs %v, shims %v", pairs, shims)
	}
	restarts := "(" + strings.Join(pairs, "; ") + ")"
	// The sentence about the shims follows the restart list directly, so that a plugin that has
	// moved cannot hide in front of the others ("kubernetes and flux have not moved").
	shimSentence := restarts + ". " + joinWith(shims, "and") + " has not moved to a plugin of its own yet"
	if len(shims) > 1 {
		shimSentence = restarts + ". " + joinWith(shims, "and") + " have not moved to plugins of their own yet"
	}

	field, _ := reflect.TypeFor[pluginconf.PluginBase]().FieldByName("Enabled")
	var table, page bytes.Buffer
	if err := config.WriteEnvTable(&table); err != nil {
		t.Fatal(err)
	}
	if err := config.WriteEnvMarkdown(&page); err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}

	tagWants := []string{"This is how " + joinWith(live, "and") + " behave", shimSentence}
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
			"applies live for " + joinWith(live, "and") + ",", restarts + ". A change to " + joinWith(shims, "or") + ",",
		}},
		{"the generated page", page.String(), []string{
			"applies live for " + joinWith(live, "and") + ",", restarts + ". A change to " + joinWith(shims, "or") + ",",
		}},
		{"the README row", string(readme), []string{"That is how " + joinWith(live, "and") + " behave", shimSentence}},
	} {
		for _, want := range missing(doc.text, doc.wants) {
			t.Errorf("%s lacks %q", doc.name, want)
		}
	}

	// The check itself: each stale form of the sentence is caught.
	tag := squash(field.Tag.Get("doc"))
	for name, stale := range map[string]string{
		"a restart pair is missing":                    strings.Replace(tag, " a gatus change restarts hardware and kubernetes;", "", 1),
		"a moved plugin is listed as one that has not": strings.Replace(tag, ". flux has not moved to a plugin", ". kubernetes and flux have not moved to plugins", 1),
		"a live plugin is missing":                     strings.Replace(tag, "proxmox, hardware, kubernetes, grafana", "proxmox, hardware, grafana", 1),
	} {
		if stale == tag {
			t.Fatalf("%s: the doc has no text to damage", name)
		}
		if len(missing(stale, tagWants)) == 0 {
			t.Errorf("%s: the check still passes", name)
		}
	}
}
