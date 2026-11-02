package proxmox

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/plugin/plugintest"
	pvemodel "github.com/TechDufus/dash/internal/plugins/proxmox/model"
	"github.com/TechDufus/dash/internal/queries"
)

// In demo mode the twin runs in place of the plugin, enabled with no configuration at all, and
// contacts nothing: a configured fake Proxmox receives no request.
func TestDemoRunsTheTwinAndContactsNoUpstream(t *testing.T) {
	pve := newFakePVE(t, false)
	e := newEnv(t, envOpts{pve: pve, demo: true})
	e.state(t, plugin.StateHealthy)
	if e.info(t).Message != "" {
		t.Errorf("message = %q", e.info(t).Message)
	}
	d, _ := e.vms(t, "the demo guests", func(d model.VMsData) bool { return len(d.VMs) > 0 })
	var n pvemodel.NodeData
	var s pvemodel.PVEStorageData
	waitFor(t, "the node and the storages", func() bool {
		e.payload(t, "proxmox.node", &n)
		e.payload(t, "proxmox.storage", &s)
		return n.Node != "" && len(s.Storages) > 0
	})

	// The same refs and flags the real plugin gives.
	plugintest.AssertRefs(t, e.sup, ID, d, model.VM{})
	plugintest.AssertRefs(t, e.sup, ID, n, pvemodel.NodeData{})
	plugintest.AssertRefs(t, e.sup, ID, s, pvemodel.PVEStorage{})
	if c := byID(d, 101); !c.Critical || c.Ref != "proxmox:VM/101" {
		t.Errorf("k8s-cp-1 = %+v, want the critical flag the demo setting gives control-plane VMs", c)
	}
	if byID(d, 110).Critical {
		t.Error("the CI runner is critical")
	}
	for _, vm := range d.VMs {
		for _, l := range vm.Links {
			if !strings.Contains(l.Href, "example.com") {
				t.Errorf("vm %d links outside example.com: %s", vm.VMID, l.Href)
			}
		}
	}

	// The charts, the detail and the links are the real code's, on the fixtures.
	if spec, id, ok := e.sup.Series("vm.cpu"); !ok || id != ID {
		t.Error("the twin serves no vm.cpu")
	} else if got, err := spec.Expr(queries.Args{VMID: "101"}); err != nil || got != `pve_cpu_usage_ratio{id="qemu/101"}` {
		t.Errorf("vm.cpu = %q, %v", got, err)
	}
	w := e.get(t, "/vms/101")
	if w.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", w.Code, w.Body)
	}
	var det model.VMDetail
	if err := json.Unmarshal(w.Body.Bytes(), &det); err != nil {
		t.Fatal(err)
	}
	if det.VM.VMID != 101 || len(det.Config.Disks) != 1 || len(det.IPs) != 1 || len(det.Snapshots) != 1 {
		t.Errorf("detail = %+v", det)
	}
	if w := e.get(t, "/vms/12345"); w.Code != http.StatusNotFound {
		t.Errorf("an unknown demo VM answered %d", w.Code)
	}
	if got := e.sup.Links().Resolve(plugin.LinkRequest{Kind: "ProxmoxUI"}); len(got) != 1 || got[0].Href != "https://pve.example.com:8006/" {
		t.Errorf("ProxmoxUI link = %+v", got)
	}

	// The contributions: KPIs from the fixtures; no template counted.
	var kpis []model.KPI
	for _, c := range e.sup.Contributors() {
		if c.ID == ID && c.KPIs != nil {
			kpis = c.KPIs.KPIs()
		}
	}
	if len(kpis) != 2 || kpis[1].Value != 5 || kpis[0].Value != 5 {
		t.Errorf("KPIs = %+v, want 5 of 5 guests running", kpis)
	}

	if got := pve.total(); got != 0 {
		t.Errorf("the twin sent %d requests to the configured Proxmox: %v", got, pve.authHeaders())
	}
}

// Every ref of every envelope the twin publishes is set: no placeholder is left empty.
func TestPayloadsCarryNoEmptyRef(t *testing.T) {
	e := newEnv(t, envOpts{demo: true})
	for _, topic := range []string{"proxmox.vms", "proxmox.node", "proxmox.storage"} {
		waitFor(t, topic, func() bool { _, ok := e.hub.Latest("", model.Topic(topic)); return ok })
		ev, _ := e.hub.Latest("", model.Topic(topic))
		var env model.Envelope
		if err := json.Unmarshal(ev.Data, &env); err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(env.Data, &v); err != nil {
			t.Fatal(err)
		}
		refs := 0
		var walk func(path string, v any)
		walk = func(path string, v any) {
			switch x := v.(type) {
			case map[string]any:
				if r, ok := x["ref"]; ok {
					refs++
					if s, _ := r.(string); s == "" {
						t.Errorf("%s: an empty ref at %s", topic, path)
					} else if p, _, _, ok := plugin.ParseRef(s); !ok || p != ID {
						t.Errorf("%s: ref %q at %s is not a proxmox ref", topic, s, path)
					}
				}
				for k, c := range x {
					walk(path+"."+k, c)
				}
			case []any:
				for i, c := range x {
					walk(path+"[]"+string(rune('0'+i%10)), c)
				}
			}
		}
		walk(topic, v)
		if refs == 0 {
			t.Errorf("%s carries no ref at all", topic)
		}
	}
}

// Stop ends the twin's goroutine and the attention, status and service keep answering from the
// real code paths.
func TestTwinStopsAndReportsLikeThePlugin(t *testing.T) {
	e := newEnv(t, envOpts{demo: true})
	pi := e.state(t, plugin.StateHealthy)
	if pi.Version != "1.0.0" || len(pi.Topics) != 3 {
		t.Errorf("row = %+v", pi)
	}
	e.sup.Shutdown(t.Context())
}
