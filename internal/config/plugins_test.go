package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/pluginconf"
)

// resolvePlugins resolves the environment (demo mode, so no public URL is needed) and an
// optional config file body.
func resolvePlugins(t *testing.T, file string, env ...string) (*Resolved, error) {
	t.Helper()
	src := Sources{Environ: append([]string{"DASH_DEMO=true"}, env...), Log: slog.New(slog.DiscardHandler)}
	if file != "" {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
		src.Environ = append(src.Environ, "DASH_CONFIG="+path)
		f, err := readConfigFile(src.Environ)
		if err != nil {
			t.Fatal(err)
		}
		src.File = f
	}
	return Resolve(src)
}

func TestPluginSectionsOrderTypeAndCopy(t *testing.T) {
	res, err := resolvePlugins(t, "plugins:\n  hardware:\n    hosts:\n      - nodename: pve1\n    enabled: false\n  kubernetes:\n    backupJobs:\n      - {namespace: backup, cronjob: nas}\n",
		"DASH_PLUGINS_PROXMOX_URL=https://pve.example.com:8006", "DASH_PLUGINS_PROXMOX_TOKEN_ID=u@pve!t", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET=sekret-0123456789")
	if err != nil {
		t.Fatal(err)
	}
	tree := res.Tree
	secs := tree.PluginSections()

	var names []string
	for _, s := range secs {
		names = append(names, s.Name)
		if s.Type != s.Name {
			t.Errorf("%s: Type = %q, want the section name when unset", s.Name, s.Type)
		}
	}
	want := []string{"prometheus", "alertmanager", "victorialogs", "gatus", "proxmox", "hardware", "headlamp", "grafana", "launcher", "kubernetes", "flux", "cnpg", "rustfs"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("sections = %v, want declaration order %v", names, want)
	}

	// Value is a pointer to a copy of the section's own type.
	pt := reflect.TypeOf(tree.Plugins)
	for i, s := range secs {
		if got, want := reflect.TypeOf(s.Value), reflect.PointerTo(pt.Field(i).Type); got != want {
			t.Errorf("%s: Value is %v, want %v", s.Name, got, want)
		}
	}

	by := map[string]PluginSection{}
	for _, s := range secs {
		by[s.Name] = s
	}
	if hw := by["hardware"]; hw.Enabled == nil || *hw.Enabled {
		t.Errorf("hardware.Enabled = %v, want an explicit false", hw.Enabled)
	}
	if by["proxmox"].Enabled != nil {
		t.Errorf("proxmox.Enabled = %v, want nil (auto)", by["proxmox"].Enabled)
	}

	// Changing the copy leaves the tree alone: strings, slices and the enabled pointer.
	px := by["proxmox"].Value.(*pluginconf.Proxmox)
	px.URL, px.Node = "https://changed.example.com", "changed"
	hw := by["hardware"].Value.(*pluginconf.Hardware)
	hw.Hosts[0].Nodename = "changed"
	hw.Hosts = append(hw.Hosts, pluginconf.HardwareHost{})
	*by["hardware"].Enabled = true
	k := by["kubernetes"].Value.(*pluginconf.Kubernetes)
	k.BackupJobs[0].Namespace = "changed"
	if tree.Plugins.Proxmox.URL != "https://pve.example.com:8006" || tree.Plugins.Proxmox.Node == "changed" ||
		tree.Plugins.Hardware.Hosts[0].Nodename != "pve1" || len(tree.Plugins.Hardware.Hosts) != 1 ||
		*tree.Plugins.Hardware.Enabled != false || tree.Plugins.Kubernetes.BackupJobs[0].Namespace != "backup" {
		t.Fatalf("a change to the copy reached the tree: %+v", tree.Plugins)
	}
	// Two calls hand out independent copies.
	if a, b := tree.PluginSections()[4].Value, tree.PluginSections()[4].Value; a == b {
		t.Error("PluginSections returned the same pointer twice")
	}

	// A secret is a handle, so a rotation of the tree's secret reaches the copy.
	if !tree.Plugins.Proxmox.TokenSecret.Update("rotated-0123456789") {
		t.Fatal("update failed")
	}
	if got := px.TokenSecret.Reveal(); got != "rotated-0123456789" {
		t.Fatalf("the copy's secret = %q after a rotation", got)
	}
}

func TestTypeMustEqualSectionName(t *testing.T) {
	for _, c := range []struct {
		name, file string
		env        []string
		wantErr    string
	}{
		{"file: another type", "plugins:\n  proxmox:\n    type: other\n", nil, "plugins.proxmox.type"},
		{"env: another plugin's type", "", []string{"DASH_PLUGINS_GRAFANA_TYPE=prometheus"}, "plugins.grafana.type"},
		{"the section name", "plugins:\n  proxmox:\n    type: proxmox\n", nil, ""},
		{"unset", "plugins:\n  proxmox:\n    node: pve\n", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			res, err := resolvePlugins(t, c.file, c.env...)
			if c.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				for _, s := range res.Tree.PluginSections() {
					if s.Type != s.Name {
						t.Errorf("%s has type %q", s.Name, s.Type)
					}
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) || !strings.Contains(err.Error(), "one instance per plugin type") {
				t.Fatalf("err = %v, want one naming %s", err, c.wantErr)
			}
			// The message says which layer set it.
			if c.file == "" && !strings.Contains(err.Error(), "DASH_PLUGINS_GRAFANA_TYPE") {
				t.Errorf("err does not name the variable: %v", err)
			}
		})
	}
}

func TestEnabledIsTriState(t *testing.T) {
	enabled := func(t *testing.T, res *Resolved, name string) *bool {
		t.Helper()
		for _, s := range res.Tree.PluginSections() {
			if s.Name == name {
				return s.Enabled
			}
		}
		t.Fatalf("no section %s", name)
		return nil
	}
	t.Run("unset is nil", func(t *testing.T) {
		res, err := resolvePlugins(t, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range res.Tree.PluginSections() {
			if s.Enabled != nil {
				t.Errorf("%s: Enabled = %v by default", s.Name, *s.Enabled)
			}
		}
		if p := res.Provenance["plugins.proxmox.enabled"]; p.Source != SourceDefault {
			t.Errorf("provenance = %+v", p)
		}
	})
	t.Run("env", func(t *testing.T) {
		res, err := resolvePlugins(t, "", "DASH_PLUGINS_PROXMOX_ENABLED=false", "DASH_PLUGINS_GRAFANA_ENABLED=true")
		if err != nil {
			t.Fatal(err)
		}
		if e := enabled(t, res, "proxmox"); e == nil || *e {
			t.Errorf("proxmox = %v, want false", e)
		}
		if e := enabled(t, res, "grafana"); e == nil || !*e {
			t.Errorf("grafana = %v, want true", e)
		}
		if p := res.Provenance["plugins.proxmox.enabled"]; p.Source != SourceEnv || p.Ref != "DASH_PLUGINS_PROXMOX_ENABLED" {
			t.Errorf("provenance = %+v", p)
		}
	})
	t.Run("file, and the environment overrides it", func(t *testing.T) {
		file := "plugins:\n  gatus:\n    enabled: false\n  flux:\n    enabled: true\n"
		res, err := resolvePlugins(t, file)
		if err != nil {
			t.Fatal(err)
		}
		if e := enabled(t, res, "gatus"); e == nil || *e {
			t.Errorf("gatus = %v, want false", e)
		}
		if e := enabled(t, res, "flux"); e == nil || !*e {
			t.Errorf("flux = %v, want true", e)
		}
		res, err = resolvePlugins(t, file, "DASH_PLUGINS_GATUS_ENABLED=true")
		if err != nil {
			t.Fatal(err)
		}
		if e := enabled(t, res, "gatus"); e == nil || !*e {
			t.Errorf("gatus = %v, want the environment's true", e)
		}
	})
	t.Run("only booleans", func(t *testing.T) {
		if _, err := resolvePlugins(t, "", "DASH_PLUGINS_PROXMOX_ENABLED=maybe"); err == nil || !strings.Contains(err.Error(), "DASH_PLUGINS_PROXMOX_ENABLED") {
			t.Errorf("err = %v", err)
		}
		if _, err := resolvePlugins(t, "plugins:\n  proxmox:\n    enabled: yes please\n"); err == nil {
			t.Error("a non-boolean in the file was accepted")
		}
	})
	t.Run("settings every plugin section has", func(t *testing.T) {
		for _, s := range (Tree{}).PluginSections() {
			for _, leaf := range []string{"type", "enabled"} {
				l := schema.byPath["plugins."+s.Name+"."+leaf]
				if l == nil {
					t.Errorf("plugins.%s.%s is not a setting", s.Name, leaf)
				} else if l.env != "DASH_PLUGINS_"+strings.ToUpper(s.Name)+"_"+strings.ToUpper(leaf) {
					t.Errorf("%s has the env name %s", l.path, l.env)
				}
			}
		}
	})
}

func TestSchemaWalkFlattensEmbeddedStruct(t *testing.T) {
	type section struct {
		pluginconf.PluginBase
		URL string `json:"url" dash:"url"`
	}
	type root struct {
		Plugins struct {
			X section `json:"x"`
		} `json:"plugins"`
	}
	s := buildSchema(reflect.TypeFor[root]())
	var paths []string
	for _, l := range s.leaves {
		paths = append(paths, l.path)
	}
	if want := []string{"plugins.x.type", "plugins.x.enabled", "plugins.x.url"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("leaves = %v, want %v", paths, want)
	}
	if s.byPath["plugins.x.pluginBase"] != nil || s.byPath["plugins.x.PluginBase"] != nil {
		t.Error("the embedded struct is a setting of its own")
	}
	if got := s.byPath["plugins.x.enabled"].env; got != "DASH_PLUGINS_X_ENABLED" {
		t.Errorf("env = %s", got)
	}
	// The leaves are settable through their index path, whatever the nesting.
	var r root
	v := reflect.ValueOf(&r).Elem()
	v.FieldByIndex(s.byPath["plugins.x.type"].index).SetString("x")
	v.FieldByIndex(s.byPath["plugins.x.url"].index).SetString("https://example.com")
	on := true
	v.FieldByIndex(s.byPath["plugins.x.enabled"].index).Set(reflect.ValueOf(&on))
	if r.Plugins.X.Type != "x" || r.Plugins.X.URL != "https://example.com" || r.Plugins.X.Enabled == nil || !*r.Plugins.X.Enabled {
		t.Fatalf("root = %+v", r)
	}
	// The section node has the three children, in order.
	node := s.root.child("plugins").child("x")
	if !reflect.DeepEqual(node.names, []string{"type", "enabled", "url"}) {
		t.Fatalf("children = %v", node.names)
	}

	// A field without a json name that is not an embedded struct still panics.
	defer func() {
		if recover() == nil {
			t.Fatal("a field without a json name was accepted")
		}
	}()
	type bad struct{ Plain string }
	buildSchema(reflect.TypeFor[bad]())
}
