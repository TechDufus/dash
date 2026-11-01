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
