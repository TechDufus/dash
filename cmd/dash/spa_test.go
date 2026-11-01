package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The SPA has one module directory per plugin that ships a page of its own, named by the plugin
// ID. A directory with no manifest is dead code (or a typo that hides a page); a manifest with
// nav entries and no module (and no generic views to render instead) is a nav entry that nothing
// can draw. Both are build mismatches, so they fail here instead of in a user's browser.
func TestSPAModulesMatchManifests(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "src", "plugins", "modules")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var modules []string
	for _, e := range entries {
		if e.IsDir() {
			modules = append(modules, e.Name())
		}
	}

	var ids []string
	for _, m := range builtinManifests() {
		ids = append(ids, string(m.ID))
		if len(m.UI.Nav) > 0 && len(m.UI.Generic) == 0 && !slices.Contains(modules, string(m.ID)) {
			t.Errorf("plugin %s declares nav entries but has no SPA module: add web/src/plugins/modules/%s", m.ID, m.ID)
		}
	}
	for _, name := range modules {
		if !slices.Contains(ids, name) {
			t.Errorf("web/src/plugins/modules/%s is not the ID of a plugin manifest (one of %v)", name, ids)
		}
	}
}
