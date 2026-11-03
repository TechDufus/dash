package config

import (
	"strings"
	"testing"
)

func TestCSPDefaultsToEnforce(t *testing.T) {
	res, _, err := resolveWith(t, Sources{Environ: []string{"DASH_DEMO=true"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Config.CSP; got != CSPEnforce {
		t.Errorf("server.csp = %q, want %q with nothing set", got, CSPEnforce)
	}
}

// DASH_SERVER_CSP and the file key both reach the flat view, and the env wins over the file.
func TestCSPModeFromEnvAndFile(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		file string
		want string
	}{
		{"report-only from the env", "report-only", "", CSPReportOnly},
		{"off from the env", "off", "", CSPOff},
		{"enforce from the env", "enforce", "", CSPEnforce},
		{"off from the file", "", `server: {csp: "off"}`, CSPOff},
		{"the env wins over the file", "report-only", `server: {csp: "off"}`, CSPReportOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := Sources{Environ: []string{"DASH_DEMO=true"}}
			if tc.env != "" {
				src.Environ = append(src.Environ, "DASH_SERVER_CSP="+tc.env)
			}
			if tc.file != "" {
				src.File = &File{Path: "c.yaml", Data: []byte(tc.file + "\n")}
			}
			res, _, err := resolveWith(t, src)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Config.CSP; got != tc.want {
				t.Errorf("server.csp = %q, want %q", got, tc.want)
			}
		})
	}
}

// An unknown mode is refused at load, naming the setting and the layer: the middleware would
// otherwise enforce it silently, which is the opposite of what a typo in "off" asks for.
func TestCSPModeEnumRejectsUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  Sources
		want []string
	}{
		{"env", Sources{Environ: []string{"DASH_DEMO=true", "DASH_SERVER_CSP=disabled"}},
			[]string{"server.csp", "(env DASH_SERVER_CSP)", `"disabled"`, "enforce, report-only, off"}},
		{"case matters", Sources{Environ: []string{"DASH_DEMO=true", "DASH_SERVER_CSP=Off"}},
			[]string{"server.csp", `"Off"`}},
		{"file", Sources{Environ: []string{"DASH_DEMO=true"}, File: &File{Path: "c.yaml", Data: []byte("server: {csp: nope}\n")}},
			[]string{"server.csp", "(file c.yaml)", `"nope"`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveWith(t, tc.src)
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not contain %q", err, w)
				}
			}
		})
	}
}

// The setting is a restart one and its environment name is the generated one.
func TestCSPSettingIsRestartAndNamed(t *testing.T) {
	if got := envName("server.csp"); got != "DASH_SERVER_CSP" {
		t.Errorf("env name = %q", got)
	}
	var found bool
	for _, l := range schema.leaves {
		if l.path == "server.csp" {
			found = true
			if !l.restart {
				t.Error("server.csp is not a restart setting")
			}
		}
	}
	if !found {
		t.Error("server.csp is not in the schema")
	}
}
