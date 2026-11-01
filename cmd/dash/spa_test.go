package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// undrawnNav is the nav paths of m that the SPA cannot draw. A module (a directory named by the
// plugin ID) draws its own routes; without one the only page is /x/<id>, and only for a manifest
// with generic views.
func undrawnNav(m plugin.Manifest, modules []string) []string {
	if slices.Contains(modules, string(m.ID)) {
		return nil
	}
	var out []string
	for _, n := range m.UI.Nav {
		if len(m.UI.Generic) > 0 && n.Path == "/x/"+string(m.ID) {
			continue
		}
		out = append(out, n.Path)
	}
	return out
}

// The SPA has one module directory per plugin that ships a page of its own, named by the plugin
// ID. A directory with no manifest is dead code (or a typo that hides a page); a nav entry that
// nothing can draw is a page that 404s from the rail. Both are build mismatches, so they fail
// here instead of in a user's browser.
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
		for _, p := range undrawnNav(m, modules) {
			t.Errorf("plugin %s declares the nav entry %s but has no SPA module that draws it: add web/src/plugins/modules/%s", m.ID, p, m.ID)
		}
	}
	for _, name := range modules {
		if !slices.Contains(ids, name) {
			t.Errorf("web/src/plugins/modules/%s is not the ID of a plugin manifest (one of %v)", name, ids)
		}
	}
}

// No built-in plugin has generic views yet, so the generic rules are checked on made-up manifests.
func TestUndrawnNav(t *testing.T) {
	view := []model.GenericView{{ID: "v", Kind: model.GenericStat}}
	nav := func(paths ...string) []model.NavItem {
		var out []model.NavItem
		for _, p := range paths {
			out = append(out, model.NavItem{Path: p})
		}
		return out
	}
	manifest := func(nav []model.NavItem, generic []model.GenericView) plugin.Manifest {
		return plugin.Manifest{ID: "queue", UI: plugin.UISpec{Nav: nav, Generic: generic}}
	}
	for name, tc := range map[string]struct {
		m       plugin.Manifest
		modules []string
		want    []string
	}{
		"no nav":                         {manifest(nil, nil), nil, nil},
		"nav and no module":              {manifest(nav("/queue"), nil), nil, []string{"/queue"}},
		"nav and a module":               {manifest(nav("/queue"), nil), []string{"queue"}, nil},
		"generic page":                   {manifest(nav("/x/queue"), view), nil, nil},
		"generic page, no views":         {manifest(nav("/x/queue"), nil), nil, []string{"/x/queue"}},
		"another plugin's generic page":  {manifest(nav("/x/other"), view), nil, []string{"/x/other"}},
		"explicit nav next to the views": {manifest(nav("/x/queue", "/queue"), view), nil, []string{"/queue"}},
		"explicit nav, views, a module":  {manifest(nav("/queue"), view), []string{"queue"}, nil},
		"explicit nav and views only":    {manifest(nav("/queue"), view), nil, []string{"/queue"}},
	} {
		if got := undrawnNav(tc.m, tc.modules); !slices.Equal(got, tc.want) {
			t.Errorf("%s: undrawn = %v, want %v", name, got, tc.want)
		}
	}
}
