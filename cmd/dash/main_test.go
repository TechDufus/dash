package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfigEnvCommandPrintsTheNameTable(t *testing.T) {
	var out bytes.Buffer
	if err := command([]string{"config", "env"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"DASH_SERVER_PUBLIC_URL", "DASH_PLUGINS_PROXMOX_TOKEN_SECRET", "KUBECONFIG"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %s:\n%s", want, &out)
		}
	}
	if strings.Contains(out.String(), "LEGACY") {
		t.Errorf("the table has no legacy column:\n%s", &out)
	}
}

func TestUnknownCommandIsRefused(t *testing.T) {
	err := command([]string{"conf", "env"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "conf env") {
		t.Errorf("error = %v", err)
	}
}

// The usage a mistyped command prints names the whole command line, and lists each command once
// with its own `dash` prefix (no "dash [dash config …").
func TestUsageTextNamesTheCommandAndEachUsageOnce(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		unknown string
	}{
		{[]string{"api"}, `"api"`},
		{[]string{"api", "nope"}, `"api nope"`},
		{[]string{"config", "nope"}, `"config nope"`},
		{[]string{"nope"}, `"nope"`},
	} {
		err := command(tc.args, &bytes.Buffer{})
		if err == nil || !strings.Contains(err.Error(), "unknown command "+tc.unknown) {
			t.Errorf("%v: error = %v, want unknown command %s", tc.args, err, tc.unknown)
			continue
		}
		if strings.Contains(err.Error(), "[]") || strings.Contains(err.Error(), "dash [") {
			t.Errorf("%v: malformed usage: %v", tc.args, err)
		}
	}
	err := command([]string{"nope"}, &bytes.Buffer{})
	for _, u := range []string{configUsage, auditUsage, apiUsage} {
		if err == nil || strings.Count(err.Error(), u) != 1 {
			t.Errorf("usage %q is not in the top-level error exactly once: %v", u, err)
		}
	}
}
