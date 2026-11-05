package config

import (
	"reflect"
	"testing"
)

func TestRedactURLs(t *testing.T) {
	in := map[string]any{
		"a": "https://user:pw@host.example.com/p?q=1",
		"b": []any{"http://token@h.example.com", "no userinfo https://h.example.com", 3.0},
		"c": "https://h.example.com/a@b",
		"d": "https://us er:p%zz@host.example.com", // does not parse
	}
	want := map[string]any{
		"a": "https://host.example.com/p?q=1",
		"b": []any{"http://h.example.com", "no userinfo https://h.example.com", 3.0},
		"c": "https://h.example.com/a@b",
		"d": "https://host.example.com",
	}
	if got := RedactURLs(in); !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

// The metadata of a leaf comes from its tags: the JSON type, enum, restart, url and dest.
func TestLeafMetadata(t *testing.T) {
	for path, want := range map[string]Leaf{
		"server.csp":                       {Type: "string", Restart: true, Ineligible: true, Enum: []string{"enforce", "report-only", "off"}},
		"plugins.proxmox.url":              {Type: "string", URL: true},
		"plugins.proxmox.tls.caFile":       {Type: "string", Dest: true},
		"plugins.proxmox.criticalVMs.ids":  {Type: "array"},
		"plugins.proxmox.enabled":          {Type: "boolean"},
		"plugins.proxmox.tokenSecret":      {Type: "string", Secret: true},
		"server.readyTimeout":              {Type: "string", Restart: true, Ineligible: true},
		"settings.secretNamespaces":        {Type: "array", Restart: true, Ineligible: true},
		"server.limits.streams":            {Type: "integer", Restart: true, Ineligible: true},
		"plugins.alertmanager.severityMap": {Type: "object"},
	} {
		got, ok := LeafAt(path)
		if !ok {
			t.Fatalf("%s: no leaf", path)
		}
		want.Path, want.Env, want.Doc = path, got.Env, got.Doc
		if got.Doc == "" || !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %+v\nwant %+v", path, got, want)
		}
	}
	if _, ok := LeafAt("plugins.proxmox"); ok {
		t.Error("a section is no leaf")
	}
}
