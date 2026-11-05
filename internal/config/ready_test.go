package config

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestReadyDefaults(t *testing.T) {
	c := Defaults()
	if len(c.ReadyRequires) != 0 || c.ReadyTimeout != 60*time.Second {
		t.Errorf("readyRequires = %v, readyTimeout = %v; want none and 60s", c.ReadyRequires, c.ReadyTimeout)
	}
}

func TestReadySettingsFromFileAndEnv(t *testing.T) {
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEMO=true", `DASH_SERVER_READY_REQUIRES=[" Kubernetes", "", "flux"]`},
		File:    &File{Path: "c.yaml", Data: []byte("server:\n  readyRequires: [gatus]\n  readyTimeout: 90s\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Config.ReadyRequires; !slices.Equal(got, []string{"kubernetes", "flux"}) {
		t.Errorf("the environment replaces the file's list whole, normalised: %v", got)
	}
	if res.Config.ReadyTimeout != 90*time.Second {
		t.Errorf("readyTimeout = %v, want 90s", res.Config.ReadyTimeout)
	}
}

func TestReadyRequiresUnknownPluginIsAConfigError(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		env        []string
		want       []string
	}{
		{"file", "server: {readyRequires: [kubernets]}", nil, []string{"server.readyRequires", "(file c.yaml)", "entry 1 is not a plugin", "kubernetes"}},
		{"env", "", []string{`DASH_SERVER_READY_REQUIRES=["nope"]`}, []string{"DASH_SERVER_READY_REQUIRES", "server.readyRequires", "entry 1 is not a plugin"}},
		{"one of several", "server: {readyRequires: [kubernetes, nope]}", nil, []string{"entry 2 is not a plugin"}},
		{"zero timeout", "server: {readyTimeout: 0s}", nil, []string{"server.readyTimeout", "greater than zero"}},
		{"negative timeout", "", []string{"DASH_SERVER_READY_TIMEOUT=-5s"}, []string{"DASH_SERVER_READY_TIMEOUT", "greater than zero"}},
		{"timeout needs a unit", "server: {readyTimeout: 60}", nil, []string{"server.readyTimeout", "duration"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Resolve(Sources{
				Environ: append([]string{"DASH_DEMO=true"}, tc.env...),
				File:    &File{Path: "c.yaml", Data: []byte(tc.file)},
			})
			if err == nil {
				t.Fatal("resolved without error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

// A disabled plugin is a valid readyRequires entry: readiness treats it as settled (and main
// warns), so a config that disables a plugin it also lists still loads.
func TestReadyRequiresMayNameADisabledPlugin(t *testing.T) {
	_, err := Resolve(Sources{
		Environ: []string{"DASH_DEMO=true"},
		File:    &File{Path: "c.yaml", Data: []byte("server: {readyRequires: [kubernetes]}\nplugins: {kubernetes: {enabled: false}}\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReadySettingsAreRestartSettingsInTheEnvTable(t *testing.T) {
	byPath := map[string]EnvVar{}
	for _, v := range EnvVars() {
		byPath[v.Path] = v
	}
	for path, env := range map[string]string{
		"server.readyRequires": "DASH_SERVER_READY_REQUIRES",
		"server.readyTimeout":  "DASH_SERVER_READY_TIMEOUT",
	} {
		if v, ok := byPath[path]; !ok || v.Name != env || !v.Restart {
			t.Errorf("%s: %+v, want env %s and restart", path, v, env)
		}
	}
	if got := byPath["server.readyTimeout"].Type; got != "duration (e.g. 30s)" {
		t.Errorf("readyTimeout type = %q", got)
	}
}

func TestUIMayNotSetReadiness(t *testing.T) {
	log, buf := logBuf()
	res, err := Resolve(Sources{
		Environ: []string{"DASH_DEMO=true"}, Log: log,
		UI: map[string]any{"server": map[string]any{"readyTimeout": "1s"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Config.ReadyTimeout != 60*time.Second || !strings.Contains(buf.String(), "server.readyTimeout") {
		t.Errorf("readyTimeout = %v, log %q: the UI layer must not reach server.*", res.Config.ReadyTimeout, buf)
	}
}
