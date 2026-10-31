// Package demo runs dash without a cluster: DASH_DEMO=true publishes a fixed, obviously fake
// homelab on every topic and answers the REST endpoints from the same fixtures. It exists for
// screenshots and UI work; nothing in it talks to Kubernetes, Proxmox or any monitoring service.
package demo

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/config"
	"github.com/TechDufus/dash/internal/hub"
	"github.com/TechDufus/dash/internal/links"
	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/topics"
	"github.com/TechDufus/dash/internal/upstream"
)

// tick is how often the fixtures drift, so the UI looks live.
const tick = 5 * time.Second

// Config points every deep link and launcher entry at example.com and names the fake host.
func Config(cfg config.Config) config.Config {
	cfg.ProxmoxURL = "https://pve.example.com:8006"
	cfg.ProxmoxNode = hostName
	cfg.HostNodename = hostName
	cfg.HeadlampURL = "https://headlamp.example.com"
	cfg.HeadlampCluster = "demo"
	cfg.GrafanaURL = "https://grafana.example.com"
	cfg.HostModel = "Dell R730"
	cfg.Launcher = []model.LauncherLink{
		{Name: "Proxmox", Href: "https://pve.example.com:8006", Icon: "proxmox", Description: "Hypervisor"},
		{Name: "iDRAC", Href: "https://idrac.example.com", Icon: "idrac", Description: "Out-of-band"},
		{Name: "UniFi", Href: "https://unifi.example.com", Icon: "unifi", Description: "Network"},
		{Name: "NAS", Href: "https://nas.example.com", Icon: "unifi-drive", Description: "Backups"},
		{Name: "GitOps repo", Href: "https://git.example.com/homelab", Icon: "github", Description: "Flux source"},
	}
	return cfg
}

// Source serves the fixtures to the hub and the API.
type Source struct {
	links *links.Builder
	start time.Time
	now   func() time.Time
}

// New returns a Source anchored at the current time.
func New(cfg config.Config) *Source {
	return &Source{links: links.New(cfg), start: time.Now(), now: time.Now}
}

// Ready is always true: there are no informers to wait for.
func (s *Source) Ready() bool { return true }

// Run publishes every topic now and again on each tick until ctx ends; it does not block.
func (s *Source) Run(ctx context.Context, h *hub.Hub) {
	s.publish(h)
	go func() {
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.publish(h)
			}
		}
	}()
}

func (s *Source) publish(h *hub.Hub) {
	w := s.build()
	at := s.now()
	src := func(names ...string) []model.SourceStatus {
		out := make([]model.SourceStatus, len(names))
		for i, n := range names {
			out[i] = model.SourceStatus{Name: n, OK: true, CheckedAt: at}
		}
		return out
	}
	for _, p := range []struct {
		topic model.Topic
		data  any
		srcs  []model.SourceStatus
	}{
		{model.TopicOverview, w.overview, src("kubernetes", "victoriametrics", "proxmox", "alertmanager")},
		{model.TopicHost, w.host, src("victoriametrics")},
		{model.TopicVMs, w.vms, src("proxmox", "victoriametrics")},
		{model.TopicCluster, w.cluster, src("kubernetes", "victoriametrics")},
		{model.TopicFlux, w.flux, src("kubernetes")},
		{model.TopicStorage, w.storage, src("kubernetes", "victoriametrics", "proxmox")},
		{model.TopicAlerts, w.alerts, src("alertmanager")},
		{model.TopicStatus, w.status, src("gatus")},
		{model.TopicApps, w.apps, src("kubernetes", "gatus")},
	} {
		if _, err := h.Publish(p.topic, p.data, p.srcs); err != nil {
			slog.Error("demo publish", "topic", p.topic, "error", err)
		}
	}
}

// wave is a smooth, repeatable drift in [-1, 1] for a key at time t.
func wave(key string, t time.Time) float64 {
	f := fnv.New32a()
	_, _ = f.Write([]byte(key))
	phase := float64(f.Sum32()%1000) / 1000 * 2 * math.Pi
	x := float64(t.Unix())
	// A daily swing, an hourly one and a little chatter, so every chart range has some shape.
	return 0.5*math.Sin(2*math.Pi*x/86400+phase) + 0.35*math.Sin(x/1700+2*phase) + 0.15*math.Sin(x/240+3*phase)
}

// around returns base drifting by up to ±spread.
func around(key string, t time.Time, base, spread float64) float64 {
	return base + spread*wave(key, t)
}

// ---- REST: series, logs, details, silences ----

// Range answers /api/series with generated lines shaped like the real query's result.
func (s *Source) Range(_ context.Context, expr string, start, end time.Time, step time.Duration) ([]model.SeriesLine, error) {
	type shape struct {
		labels       map[string]string
		base, spread float64
	}
	one := func(base, spread float64) []shape { return []shape{{map[string]string{}, base, spread}} }
	var shapes []shape
	switch {
	case strings.Contains(expr, "ipmi_fan_speed_rpm"):
		for i, rpm := range []float64{5880, 6000, 5760, 6120, 5940, 6060} {
			shapes = append(shapes, shape{map[string]string{"name": fmt.Sprintf("Fan%d", i+1)}, rpm, 240})
		}
	case strings.Contains(expr, "ipmi_temperature_celsius"):
		for _, x := range hostSensors {
			shapes = append(shapes, shape{map[string]string{"name": x.Name}, x.ValueC, 3})
		}
	case strings.Contains(expr, "ipmi_power_watts"):
		shapes = one(214, 26)
	case strings.Contains(expr, "node_hwmon_temp_celsius"):
		shapes = one(57, 6)
	case strings.Contains(expr, "pve_cpu_usage_ratio"), strings.Contains(expr, "node_cpu_seconds_total"):
		shapes = one(0.22, 0.12)
	case strings.Contains(expr, "pve_memory"), strings.Contains(expr, "node_memory"):
		shapes = one(0.61, 0.06)
	case strings.Contains(expr, "network_receive"):
		shapes = one(3.2e6, 2.4e6)
	case strings.Contains(expr, "network_transmit"):
		shapes = one(1.4e6, 1.1e6)
	case strings.Contains(expr, "disk_read"):
		shapes = one(8.5e5, 7e5)
	case strings.Contains(expr, "disk_written"):
		shapes = one(2.1e6, 1.6e6)
	case strings.Contains(expr, "container_cpu_usage"):
		shapes = one(0.045, 0.03)
	case strings.Contains(expr, "container_memory"):
		shapes = one(1.8e8, 2.5e7)
	default:
		shapes = one(0.5, 0.2)
	}
	out := make([]model.SeriesLine, 0, len(shapes))
	for _, sh := range shapes {
		key := expr + fmt.Sprint(sh.labels)
		var pts [][2]float64
		for t := start.Truncate(step); !t.After(end); t = t.Add(step) {
			pts = append(pts, [2]float64{float64(t.Unix()), math.Max(0, around(key, t, sh.base, sh.spread))})
		}
		out = append(out, model.SeriesLine{Labels: sh.labels, Points: pts})
	}
	return out, nil
}

// Query answers /api/logs with a few minutes of plausible lines for the pod.
func (s *Source) Query(_ context.Context, q upstream.LogQuery) ([]model.LogLine, error) {
	now := s.now()
	lines := make([]model.LogLine, 0, 60)
	for i := 59; i >= 0; i-- {
		lines = append(lines, logLine(q, now.Add(-time.Duration(i)*7*time.Second), i))
	}
	return lines, nil
}

// Tail emits a new line every couple of seconds until the client goes away.
func (s *Source) Tail(ctx context.Context, q upstream.LogQuery, emit func(model.LogLine) error) error {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			if err := emit(logLine(q, now, i)); err != nil {
				return err
			}
		}
	}
}

var logMsgs = []string{
	`level=info msg="request served" method=GET path=/healthz status=200 duration=1.2ms`,
	`level=info msg="reconcile finished" changed=0 duration=48ms`,
	`level=info msg="cache synced" kind=Pod items=212`,
	`level=debug msg="watch event" type=MODIFIED object=demo/web-7d9c`,
	`level=info msg="request served" method=GET path=/api/stream status=200 duration=31.4s`,
	`level=warn msg="slow upstream" target=metrics duration=1.8s`,
}

func logLine(q upstream.LogQuery, at time.Time, i int) model.LogLine {
	pod := q.Pod
	if pod == "" {
		pod = "demo"
	}
	c := q.Container
	if c == "" {
		c = "app"
	}
	return model.LogLine{Time: at.UTC(), Pod: pod, Container: c, Msg: logMsgs[i%len(logMsgs)]}
}

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

// CreateSilence pretends to create a silence.
func (s *Source) CreateSilence(context.Context, upstream.SilenceSpec) (string, error) {
	return "demo-silence", nil
}

// ExpireSilence pretends to expire a silence.
func (s *Source) ExpireSilence(context.Context, string) error { return nil }

// Wrap signs every request in as the user "demo" and answers the Kubernetes actions with 202
// without doing anything, since demo mode has no cluster to patch. Silences go through the API
// to CreateSilence/ExpireSilence above.
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
