// Package demo runs dash without a cluster: DASH_DEMO=true publishes a fixed, obviously fake
// homelab on every topic and answers the REST endpoints from the same fixtures. It exists for
// screenshots and UI work; nothing in it talks to Kubernetes, Proxmox or any monitoring service.
package demo

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/demo/world"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
	"github.com/TechDufus/dash/internal/topics"
)

// tick is how often the fixtures drift, so the UI looks live.
const tick = 5 * time.Second

// Config points the Proxmox link at example.com and names the fake host. The links into Grafana
// and Headlamp, and the launcher, are those of the plugins' demo twins (world.GrafanaURL, …).
func Config(cfg config.Config) config.Config {
	cfg.ProxmoxURL = "https://pve.example.com:8006"
	cfg.ProxmoxNode = world.HostName
	cfg.HostNodename = world.HostName
	return cfg
}

// Source serves the fixtures to the hub and the API.
type Source struct {
	// HasReal reports whether a plugin has a real factory (and so publishes its own demo data
	// through its twin). Nil means none does. Set it before Plugins.
	HasReal func(id plugin.ID) bool

	world *world.World
	start time.Time
	now   func() time.Time
	hosts plugin.HostProvider
}

// New returns a Source anchored at the current time. Call Plugins before Run.
func New(cfg config.Config) *Source {
	s := &Source{start: time.Now(), now: time.Now}
	s.world = world.New(func() time.Time { return s.now() }, s.start, newLinks(cfg))
	return s
}

// Plugins returns the demo shims of the legacy manifests, all healthy: the demo has nothing to
// poll. Unlike the production shims they can be switched off and on at runtime. Each topic is
// published through the Host of its plugin in hosts (the supervisor they are added to), so
// disabling a plugin in demo mode removes its topics like it does for real. A plugin for which
// HasReal reports a factory is left out and its topics are not published here: its demo twin
// builds them from the world, so a twin and a shim never both register.
func (s *Source) Plugins(hosts plugin.HostProvider) []plugin.Factory {
	s.hosts = hosts
	ms := topics.LegacyManifests()
	out := make([]plugin.Factory, 0, len(ms))
	for _, m := range ms {
		if s.real(m.ID) {
			continue
		}
		out = append(out, func() plugin.Plugin { return demoPlugin{m} })
	}
	return out
}

// real reports whether the plugin has a real factory, and with it a demo twin.
func (s *Source) real(id plugin.ID) bool { return s.HasReal != nil && s.HasReal(id) }

// Run publishes every topic now and again on each tick until ctx ends; it does not block. Plugins
// must have been called.
func (s *Source) Run(ctx context.Context) {
	s.publish()
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.publish()
			}
		}
	}()
}

func (s *Source) publish() {
	w := s.build()
	at := s.now()
	src := func(names ...string) []model.SourceStatus {
		out := make([]model.SourceStatus, len(names))
		for i, n := range names {
			out[i] = model.SourceStatus{Name: n, OK: true, CheckedAt: at}
		}
		return out
	}
	// The alertmanager plugin's KPI and its source chip are on the overview while its twin runs.
	ov, ovSrcs := w.overview, src("kubernetes", "victoriametrics", "proxmox")
	if s.hosts.Running("alertmanager") {
		ovSrcs = append(ovSrcs, src("alertmanager")...)
	} else {
		ov.KPIList = slices.DeleteFunc(slices.Clone(ov.KPIList), func(k model.KPI) bool { return k.Plugin == "alertmanager" })
	}
	for _, p := range []struct {
		topic model.Topic
		data  any
		srcs  []model.SourceStatus
	}{
		{model.TopicOverview, ov, ovSrcs},
		{model.TopicHost, w.host, src("victoriametrics")},
		{model.TopicVMs, w.vms, src("proxmox", "victoriametrics")},
		{model.TopicCluster, w.cluster, src("kubernetes", "victoriametrics")},
		{model.TopicFlux, w.flux, src("kubernetes")},
		{model.TopicStorage, w.storage, src("kubernetes", "victoriametrics", "proxmox")},
		{model.TopicApps, w.apps, src("kubernetes", "gatus")},
	} {
		if id, local, ok := topics.Owner(p.topic); ok {
			if s.hosts.Running(id) && !s.real(id) { // a disabled plugin publishes nothing; a twin publishes for itself
				s.hosts.HostFor(id).Publish(local, p.data, p.srcs)
			}
		} else {
			s.hosts.HostFor(plugin.Core).Publish(strings.TrimPrefix(string(p.topic), string(plugin.Core)+"."), p.data, p.srcs)
		}
	}
}

// ---- REST: details ----

// PodDetail answers /api/pods/{ns}/{name} from the cluster fixtures.
func (s *Source) PodDetail(_ context.Context, ns, name string) (model.PodDetail, error) {
	w := s.build()
	for _, p := range w.cluster.Pods {
		if p.Namespace != ns || p.Name != name {
			continue
		}
		var chain []model.OwnerRef
		if p.Owner != nil {
			if p.Owner.Kind == string(model.WorkloadKindDeployment) {
				chain = append(chain, model.OwnerRef{Kind: "ReplicaSet", Name: p.Owner.Name + "-7d9c5b8f4"})
			}
			chain = append(chain, *p.Owner)
		}
		state, ready := "running", p.ReadyContainers == p.TotalContainers
		if !ready {
			state = "waiting: " + p.Reason
		}
		var events []model.Event
		for _, e := range w.cluster.Events {
			if e.Namespace == ns && strings.HasSuffix(e.Object, "/"+name) {
				events = append(events, e)
			}
		}
		return model.PodDetail{
			Pod: p,
			IPs: []string{fmt.Sprintf("198.51.100.%d", 10+len(name)%200)},
			Conditions: []model.Condition{
				{Type: "PodScheduled", Status: "True"},
				{Type: "Initialized", Status: "True"},
				{Type: "ContainersReady", Status: boolStatus(ready)},
				{Type: "Ready", Status: boolStatus(ready)},
			},
			Containers: []model.Container{{
				Name: "app", Image: imageOf(w.cluster.Workloads, p), State: state, Ready: ready,
				Restarts: p.Restarts,
			}},
			OwnerChain: chain,
			Events:     events,
		}, nil
	}
	return model.PodDetail{}, topics.ErrNotFound
}

func boolStatus(b bool) string {
	if b {
		return "True"
	}
	return "False"
}

func imageOf(ws []model.Workload, p model.Pod) string {
	for _, w := range ws {
		if p.Owner != nil && w.Namespace == p.Namespace && w.Name == p.Owner.Name && len(w.Images) > 0 {
			return w.Images[0]
		}
	}
	return "ghcr.io/example/app:1.0.0"
}

// NodeIP resolves a demo node for the node series.
func (s *Source) NodeIP(name string) (string, bool) {
	for _, n := range s.build().cluster.Nodes {
		if n.Name == name {
			return n.IP, true
		}
	}
	return "", false
}

// VMDetail answers /api/vms/{vmid} from the VM fixtures.
func (s *Source) VMDetail(_ context.Context, vmid int) (model.VMDetail, error) {
	for _, vm := range s.build().vms.VMs {
		if vm.VMID != vmid {
			continue
		}
		snap := s.now().Add(-26 * time.Hour)
		d := model.VMDetail{
			VM: vm,
			Config: model.VMConfig{
				Cores: int(vm.CPUs), Sockets: 1, MemoryMiB: int(vm.MaxMemBytes >> 20),
				BalloonMiB: 0, OSType: "l26", Agent: "1",
				Disks: []model.KeySpec{{Key: "scsi0", Spec: "local-zfs:vm-" + fmt.Sprint(vmid) + "-disk-0,size=64G,ssd=1"}},
				NICs:  []model.KeySpec{{Key: "net0", Spec: "virtio=BC:24:11:00:00:" + fmt.Sprintf("%02X", vmid%256) + ",bridge=vmbr0"}},
			},
			IPs:       []model.IfaceAddr{},
			Snapshots: []model.Snapshot{{Name: "pre-upgrade", Time: &snap, Description: "before the last Talos upgrade"}},
		}
		if vm.Status == "running" {
			d.IPs = []model.IfaceAddr{{Iface: "eth0", Address: fmt.Sprintf("192.0.2.%d", vmid%200)}}
		}
		return d, nil
	}
	return model.VMDetail{}, topics.ErrNotFound
}

// Wrap signs every request in as the user "demo" and answers the Kubernetes actions with 202
// without doing anything, since demo mode has no cluster to patch. The silence actions are the
// alertmanager twin's.
func Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-User") == "" {
			r.Header.Set("X-Forwarded-User", "demo")
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/actions/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"accepted"}` + "\n"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
