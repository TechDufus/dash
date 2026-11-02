package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugins/proxmox"
	"github.com/TechDufus/dash/internal/secret"
)

// proxmoxEnv is a non-default value for every setting of plugins.proxmox, by schema path.
func proxmoxEnv(url string) map[string]string {
	return map[string]string{
		"plugins.proxmox.type":                   "proxmox",
		"plugins.proxmox.enabled":                "true",
		"plugins.proxmox.url":                    url,
		"plugins.proxmox.node":                   "pve",
		"plugins.proxmox.tokenID":                "u@pve!t",
		"plugins.proxmox.tokenSecret":            "token-secret-0123456789",
		"plugins.proxmox.tls.caFile":             "/etc/dash/pve-ca.pem",
		"plugins.proxmox.tls.insecureSkipVerify": "true",
		"plugins.proxmox.criticalVMs.ids":        "[101,2020]",
		"plugins.proxmox.criticalVMs.tags":       `["prod","DB"]`,
		"plugins.proxmox.storage.source":         "prometheus",
		"plugins.proxmox.series.vmSelector":      `vmid="{vmid}"`,
	}
}

// Every setting of plugins.proxmox set in the environment arrives, through the real environment
// layer and treeSettings.Instances(), in the configuration the plugin is started with, and the
// ones that shape a payload (criticalVMs) show in what the running plugin publishes.
func TestPR2SettingsReachTheProxmoxPlugin(t *testing.T) {
	pve := newFakePVE(t)
	env := proxmoxEnv(pve.URL)

	// The table covers the whole section, so a new key cannot be added without a row here.
	var paths []string
	for _, v := range config.EnvVars() {
		if strings.HasPrefix(v.Path, "plugins.proxmox.") {
			paths = append(paths, v.Path)
		}
	}
	slices.Sort(paths)
	var rows []string
	for p := range env {
		rows = append(rows, p)
	}
	slices.Sort(rows)
	if !slices.Equal(paths, rows) {
		t.Fatalf("plugins.proxmox settings %v, this test sets %v", paths, rows)
	}

	var more []string
	for _, v := range config.EnvVars() {
		if val, ok := env[v.Path]; ok {
			more = append(more, v.Name+"="+val)
		}
	}
	startEnv(t, t.TempDir(), more...)
	res, store, err := loadConfig(secret.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	store.Close() // the second load below takes the data directory's lock again

	var got *plugin.InstanceConfig
	for _, ic := range (treeSettings{func() config.Tree { return res.Tree }}).Instances() {
		if ic.Name == "proxmox" {
			got = &ic
		}
	}
	if got == nil {
		t.Fatal("no proxmox instance")
	}
	c, ok := got.Config.(interface{ StorageSource() string })
	if !ok {
		t.Fatalf("proxmox config is %T", got.Config)
	}
	p := res.Tree.Plugins.Proxmox
	if c.StorageSource() != "prometheus" || p.VMSelector() != `vmid="{vmid}"` || p.TLS.CAFile != "/etc/dash/pve-ca.pem" || !p.TLS.InsecureSkipVerify ||
		!slices.Equal(p.CriticalVMs.IDs, []int{101, 2020}) || !slices.Equal(p.CriticalVMs.Tags, []string{"prod", "DB"}) ||
		p.URL != pve.URL || p.Node != "pve" || p.TokenID != "u@pve!t" || p.TokenSecret.Reveal() != "token-secret-0123456789" {
		t.Fatalf("plugins.proxmox = %+v", p)
	}

	// The running plugin, started by the framework with that instance: VM 101 is critical by id.
	// (storage.source is left at its default so the fake needs no prometheus.)
	t.Setenv("DASH_PLUGINS_PROXMOX_STORAGE_SOURCE", "api")
	t.Setenv("DASH_PLUGINS_PROXMOX_TLS_CA_FILE", "")
	t.Setenv("DASH_PLUGINS_PROXMOX_TLS_INSECURE_SKIP_VERIFY", "false")
	fw, _ := loadFramework(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := fw.start(ctx, []plugin.Factory{proxmox.New}, func() {}); err != nil {
		t.Fatal(err)
	}
	defer fw.sup.Shutdown(context.Background())
	waitFor(t, func() bool {
		ev, ok := fw.hub.Latest("", model.TopicVMs)
		if !ok {
			return false
		}
		var e model.Envelope
		var d model.VMsData
		if json.Unmarshal(ev.Data, &e) != nil || json.Unmarshal(e.Data, &d) != nil {
			return false
		}
		return len(d.VMs) == 1 && d.VMs[0].VMID == 101 && d.VMs[0].Critical && d.VMs[0].Ref == "proxmox:VM/101"
	})
}
