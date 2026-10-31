package demo

import (
	"fmt"
	"strings"
	"time"

	"github.com/TechDufus/dash/internal/model"
)

// Every name, address and serial below is invented. Addresses come from the documentation
// ranges (192.0.2.0/24, 198.51.100.0/24) and every link points at example.com.

const hostName = "pve-demo"

var hostSensors = []model.Sensor{
	{Name: "Inlet Temp", ValueC: 22},
	{Name: "Exhaust Temp", ValueC: 34},
	{Name: "CPU1 Temp", ValueC: 56},
	{Name: "CPU2 Temp", ValueC: 53},
}

type world struct {
	overview model.OverviewData
	host     model.HostData
	vms      model.VMsData
	cluster  model.ClusterData
	flux     model.FluxData
	storage  model.StorageData
	alerts   model.AlertsData
	status   model.StatusData
	apps     model.AppsData
}

type nsSpec struct {
	name      string
	group     model.Group
	protected bool
	tenant    string
	apps      []appSpec
}

type appSpec struct {
	name, image string
	kind        model.WorkloadKind
	replicas    int
}

var namespaces = []nsSpec{
	{"kube-system", model.GroupPlatform, true, "", []appSpec{
		{"cilium-operator", "quay.io/cilium/operator-generic:v1.18.2", model.WorkloadKindDeployment, 1},
		{"coredns", "registry.k8s.io/coredns/coredns:v1.12.3", model.WorkloadKindDeployment, 2},
		{"cilium", "quay.io/cilium/cilium:v1.18.2", model.WorkloadKindDaemonSet, 3},
	}},
	{"flux-system", model.GroupPlatform, true, "", []appSpec{
		{"source-controller", "ghcr.io/fluxcd/source-controller:v1.7.1", model.WorkloadKindDeployment, 1},
		{"kustomize-controller", "ghcr.io/fluxcd/kustomize-controller:v1.7.0", model.WorkloadKindDeployment, 1},
		{"helm-controller", "ghcr.io/fluxcd/helm-controller:v1.4.1", model.WorkloadKindDeployment, 1},
	}},
	{"cert-manager", model.GroupPlatform, true, "", []appSpec{
		{"cert-manager", "quay.io/jetstack/cert-manager-controller:v1.19.1", model.WorkloadKindDeployment, 1},
	}},
	{"external-secrets", model.GroupPlatform, true, "", []appSpec{
		{"external-secrets", "ghcr.io/external-secrets/external-secrets:v0.20.3", model.WorkloadKindDeployment, 1},
	}},
	{"monitoring", model.GroupPlatform, false, "", []appSpec{
		{"vmsingle", "victoriametrics/victoria-metrics:v1.127.0", model.WorkloadKindDeployment, 1},
		{"vmalertmanager", "prom/alertmanager:v0.28.1", model.WorkloadKindStatefulSet, 1},
		{"grafana", "grafana/grafana:12.2.0", model.WorkloadKindDeployment, 1},
		{"node-exporter", "quay.io/prometheus/node-exporter:v1.9.1", model.WorkloadKindDaemonSet, 3},
	}},
	{"cnpg-system", model.GroupPlatform, false, "", []appSpec{
		{"cnpg-controller-manager", "ghcr.io/cloudnative-pg/cloudnative-pg:1.27.0", model.WorkloadKindDeployment, 1},
	}},
	{"dash", model.GroupApps, false, "", []appSpec{
		{"dash", "registry.example.com/dash/dash:0.4.0", model.WorkloadKindDeployment, 1},
	}},
	{"gatus", model.GroupApps, false, "", []appSpec{
		{"gatus", "ghcr.io/twin/gatus:v5.26.0", model.WorkloadKindDeployment, 1},
	}},
	{"headlamp", model.GroupApps, false, "", []appSpec{
		{"headlamp", "ghcr.io/headlamp-k8s/headlamp:v0.36.0", model.WorkloadKindDeployment, 1},
	}},
	{"homepage", model.GroupApps, false, "", []appSpec{
		{"homepage", "ghcr.io/gethomepage/homepage:v1.5.0", model.WorkloadKindDeployment, 1},
	}},
	{"registry", model.GroupApps, false, "", []appSpec{
		{"zot", "ghcr.io/project-zot/zot-linux-amd64:v2.1.8", model.WorkloadKindStatefulSet, 1},
	}},
	{"acme", model.GroupTenants, false, "acme", []appSpec{
		{"web", "registry.example.com/acme/web:2.3.1", model.WorkloadKindDeployment, 2},
		{"api", "registry.example.com/acme/api:2.3.1", model.WorkloadKindDeployment, 2},
		{"worker", "registry.example.com/acme/worker:2.3.1", model.WorkloadKindDeployment, 1},
	}},
}

var nodeNames = []string{"talos-cp-1", "talos-cp-2", "talos-cp-3"}

// crashing is the one unhealthy pod, so the attention list has something to show.
const crashingNS, crashingOwner = "acme", "worker"

func (s *Source) build() world {
	now := s.now()
	ago := func(d time.Duration) time.Time { return now.Add(-d).UTC() }
	agoP := func(d time.Duration) *time.Time { t := ago(d); return &t }
	f64 := func(v float64) *float64 { return &v }
	i64 := func(v int64) *int64 { return &v }
	gib := func(v float64) int64 { return int64(v * (1 << 30)) }
	yes := true
	var w world

	// ---- cluster ----
	podsByNode := map[string]int{}
	groupPods := map[model.Group]map[string]int{}
	for _, ns := range namespaces {
		nsm := model.Namespace{Name: ns.name, Group: ns.group, Protected: ns.protected, Tenant: ns.tenant,
			Privileged: ns.name == "kube-system" || ns.name == "monitoring", Health: model.HealthOK}
		for ai, a := range ns.apps {
			desired, ready := a.replicas, a.replicas
			h := model.HealthOK
			if ns.name == crashingNS && a.name == crashingOwner {
				ready, h = 0, model.HealthCrit
				nsm.WorkloadsNotReady++
				nsm.Health = model.HealthCrit
			}
			w.cluster.Workloads = append(w.cluster.Workloads, model.Workload{
				Kind: a.kind, Namespace: ns.name, Name: a.name, Ready: ready, Desired: desired,
				Images: []string{a.image}, Health: h, Restartable: true, Protected: ns.protected,
				Links: s.links.Headlamp(string(a.kind), ns.name, a.name),
			})
			for r := 0; r < desired; r++ {
				node := nodeNames[(ai+r+len(ns.name))%len(nodeNames)]
				suffix := fmt.Sprintf("%05x", (len(a.name)*7919+r*104729+ai*31)%0xfffff)
				name := a.name + "-" + suffix
				if a.kind == model.WorkloadKindStatefulSet {
					name = fmt.Sprintf("%s-%d", a.name, r)
				}
				if a.kind == model.WorkloadKindDeployment {
					name = a.name + "-7d9c5b8f4-" + suffix[:5]
				}
				if a.kind == model.WorkloadKindDaemonSet {
					node = nodeNames[r%len(nodeNames)]
				}
				key := ns.name + "/" + name
				cpu := around(key+"cpu", now, 0.03+0.01*float64(len(a.name)%5), 0.015)
				mem := around(key+"mem", now, float64(48+len(a.name)*9)*(1<<20), 6*(1<<20))
				p := model.Pod{
					Namespace: ns.name, Name: name, Phase: "Running", ReadyContainers: 1, TotalContainers: 1,
					Node: node, CreatedAt: ago(time.Duration(20+ai*13+r) * time.Hour),
					CPUCores: f64(cpu), MemBytes: f64(mem),
					Owner:  &model.OwnerRef{Kind: string(a.kind), Name: a.name},
					Health: model.HealthOK, Links: s.links.Headlamp("Pod", ns.name, name),
				}
				if h == model.HealthCrit {
					p.ReadyContainers, p.Restarts, p.Reason, p.Health = 0, 14, "CrashLoopBackOff", model.HealthCrit
					p.CreatedAt = ago(47 * time.Minute)
				}
				w.cluster.Pods = append(w.cluster.Pods, p)
				nsm.PodsTotal++
				if p.Health == model.HealthOK {
					nsm.PodsRunning++
				}
				nsm.Restarts += p.Restarts
				podsByNode[node]++
				if groupPods[ns.group] == nil {
					groupPods[ns.group] = map[string]int{}
				}
				groupPods[ns.group][node]++
			}
		}
		w.cluster.Namespaces = append(w.cluster.Namespaces, nsm)
	}
	for i, n := range nodeNames {
		w.cluster.Nodes = append(w.cluster.Nodes, model.Node{
			Name: n, IP: fmt.Sprintf("192.0.2.%d", 11+i), Ready: true, KubeletVersion: "v1.34.1",
			CPURatio: around(n+"cpu", now, 0.24+0.05*float64(i), 0.08),
			MemRatio: around(n+"mem", now, 0.58+0.04*float64(i), 0.04),
			Pods:     podsByNode[n], Links: s.links.Headlamp("Node", "", n),
		})
	}
	var crashPod string
	for _, p := range w.cluster.Pods {
		if p.Health == model.HealthCrit {
			crashPod = p.Name
		}
	}
	w.cluster.Events = []model.Event{
		{Namespace: crashingNS, Object: "Pod/" + crashPod, Reason: "BackOff", Message: "Back-off restarting failed container app", Count: 14, LastSeen: ago(40 * time.Second)},
		{Namespace: "acme", Object: "Deployment/web", Reason: "ScalingReplicaSet", Message: "Scaled up replica set web-7d9c5b8f4 from 1 to 2", Count: 1, LastSeen: ago(18 * time.Minute)},
		{Namespace: "monitoring", Object: "Pod/vmsingle-7d9c5b8f4-1a2b3", Reason: "Pulled", Message: "Container image already present on machine", Count: 1, LastSeen: ago(3 * time.Hour)},
		{Namespace: "cert-manager", Object: "Certificate/wildcard-example", Reason: "Issued", Message: "The certificate has been successfully issued", Count: 1, LastSeen: ago(6 * time.Hour)},
	}
	for _, c := range []struct {
		ns, name string
		days     int
	}{{"cert-manager", "wildcard-example", 61}, {"dash", "dash-tls", 74}, {"acme", "acme-web-tls", 12}} {
		w.cluster.Certificates = append(w.cluster.Certificates, model.Cert{
			Namespace: c.ns, Name: c.name, Ready: true, NotAfter: agoP(-time.Duration(c.days) * 24 * time.Hour),
			Links: s.links.Headlamp("Certificate", c.ns, c.name),
		})
	}
	w.cluster.ExternalSecrets = []model.ExtSecret{
		{Namespace: "dash", Name: "dash-oauth", Ready: true, Message: "secret synced"},
		{Namespace: "monitoring", Name: "grafana-admin", Ready: true, Message: "secret synced"},
		{Namespace: "acme", Name: "acme-db", Ready: true, Message: "secret synced"},
	}

	// ---- vms ----
	type vmSpec struct {
		id      int
		name    string
		cpus    float64
		memGiB  float64
		running bool
		tags    []string
		node    string
	}
	vmSpecs := []vmSpec{
		{101, "talos-cp-1", 4, 16, true, []string{"talos", "k8s"}, "talos-cp-1"},
		{102, "talos-cp-2", 4, 16, true, []string{"talos", "k8s"}, "talos-cp-2"},
		{103, "talos-cp-3", 4, 16, true, []string{"talos", "k8s"}, "talos-cp-3"},
		{110, "ci-runner-1", 8, 12, true, []string{"ci"}, ""},
		{120, "dev-sandbox", 2, 4, true, []string{"scratch"}, ""},
		{9000, "talos-template", 2, 4, false, []string{"template"}, ""},
	}
	for _, v := range vmSpecs {
		vm := model.VM{
			VMID: v.id, Name: v.name, Status: "stopped", Health: model.HealthOK, Tags: v.tags, CPUs: v.cpus,
			MaxMemBytes: gib(v.memGiB), Template: v.id == 9000, Lock: "",
			Links: []model.Link{s.links.VMConsole(v.id, v.name)},
		}
		if v.running {
			vm.Status = "running"
			vm.CPURatio = around(v.name+"cpu", now, 0.21, 0.09)
			vm.MemBytes = int64(float64(vm.MaxMemBytes) * around(v.name+"mem", now, 0.62, 0.05))
			vm.UptimeSeconds = int64(now.Sub(s.start).Seconds()) + 12*86400 + int64(v.id)*977
			vm.NetInBytes = 41<<30 + int64(v.id)<<24
			vm.NetOutBytes = 17<<30 + int64(v.id)<<23
		}
		if v.node != "" {
			vm.K8sNode = &model.K8sNodeRef{Name: v.node, Ready: true}
		}
		w.vms.VMs = append(w.vms.VMs, vm)
	}
	w.vms.Machines = []model.Machine{
		{Name: "rpi-control", Up: true, Source: "node-exporter"},
		{Name: "mac-mini-1", Up: true, Source: "node-exporter"},
		{Name: "router-1", Up: true, Source: "node-exporter"},
	}
	w.vms.Tasks = []model.PVETask{
		{UPID: "UPID:pve-demo:0001:qmsnapshot", Type: "qmsnapshot", ID: "101", User: "root@pam", Status: "OK", StartTime: ago(26 * time.Hour), EndTime: agoP(26*time.Hour - 40*time.Second)},
		{UPID: "UPID:pve-demo:0002:vzdump", Type: "vzdump", ID: "110", User: "root@pam", Status: "OK", StartTime: ago(9 * time.Hour), EndTime: agoP(9*time.Hour - 6*time.Minute)},
		{UPID: "UPID:pve-demo:0003:qmstop", Type: "qmstop", ID: "120", User: "root@pam", Status: "OK", StartTime: ago(3 * 24 * time.Hour), EndTime: agoP(3*24*time.Hour - 5*time.Second)},
	}

	// ---- host ----
	temps := []float64{around("pkg0", now, 57, 5), around("pkg1", now, 54, 5)}
	sensors := make([]model.Sensor, len(hostSensors))
	for i, x := range hostSensors {
		sensors[i] = model.Sensor{Name: x.Name, ValueC: around(x.Name, now, x.ValueC, 2)}
	}
	power := around("power", now, 214, 26)
	memTotal := gib(256)
	memUsed := int64(float64(memTotal) * around("hostmem", now, 0.41, 0.02))
	w.host = model.HostData{
		Name: hostName, UptimeSeconds: int64(now.Sub(s.start).Seconds()) + 38*86400,
		PVEVersion: "9.0.11", Kernel: "6.14.11-4-pve", CPUModel: "2 × Intel Xeon E5-2690 v4 @ 2.60GHz",
		CPURatio: around("hostcpu", now, 0.19, 0.07), MemUsedBytes: memUsed, MemTotalBytes: memTotal,
		PowerWatts: power, PackageTempsC: temps, Sensors: sensors,
		PSUs:         []model.PSU{{Name: "PS1 Status", State: model.HealthOK}, {Name: "PS2 Status", State: model.HealthOK}},
		SELFreeBytes: 14 << 10,
		Pools: []model.Pool{
			{Name: "rpool", State: "ONLINE", LastScrubEnd: agoP(5 * 24 * time.Hour)},
			{Name: "tank", State: "ONLINE", LastScrubEnd: agoP(5*24*time.Hour - 3*time.Hour)},
		},
		Links: []model.Link{{Label: "Proxmox", Href: "https://pve.example.com:8006", Kind: model.LinkKindProxmox}},
	}
	for i := range 6 {
		w.host.Fans = append(w.host.Fans, model.Fan{Name: fmt.Sprintf("Fan%d", i+1), RPM: around(fmt.Sprint("fan", i), now, 5900+float64(i%3)*60, 180)})
	}
	for i := range 8 {
		life := 100 - float64(3+i%4)
		if i >= 6 {
			life = 0 // spinning disks report no wear
		}
		d := model.Disk{
			Bay: fmt.Sprintf("Bay %d", i), Serial: fmt.Sprintf("DEMO%04dX", 1000+i*37),
			Device: fmt.Sprintf("/dev/sd%c", 'a'+i), Healthy: true, TempC: f64(around(fmt.Sprint("disk", i), now, 33+float64(i%3), 1.5)),
		}
		if life > 0 {
			d.LifeRemainingPct = f64(life)
		}
		w.host.Disks = append(w.host.Disks, d)
	}

	// ---- flux ----
	verified := true
	rev := "main@sha1:4f2a9c1e"
	w.flux.Sources = []model.FluxObj{
		{Kind: model.FluxKindGitRepository, Namespace: "flux-system", Name: "flux-system", Ready: true, Revision: rev, Message: "stored artifact for revision '" + rev + "'", LastTransition: agoP(11 * time.Minute), Verified: &verified},
		{Kind: model.FluxKindOCIRepository, Namespace: "flux-system", Name: "cilium", Ready: true, Revision: "1.18.2@sha256:9b1d", Message: "stored artifact", LastTransition: agoP(4 * 24 * time.Hour)},
		{Kind: model.FluxKindHelmRepository, Namespace: "flux-system", Name: "grafana", Ready: true, Revision: "sha256:1c7e", Message: "stored artifact", LastTransition: agoP(2 * time.Hour)},
		{Kind: model.FluxKindHelmRepository, Namespace: "flux-system", Name: "jetstack", Ready: true, Revision: "sha256:aa30", Message: "stored artifact", LastTransition: agoP(2 * time.Hour)},
	}
	for i, k := range []string{"platform-crds", "platform", "monitoring", "apps", "tenants", "guardrails"} {
		ks := model.FluxObj{Kind: model.FluxKindKustomization, Namespace: "flux-system", Name: k, Ready: true, Revision: rev,
			Message: "Applied revision: " + rev, LastTransition: agoP(time.Duration(11+i) * time.Minute),
			Links: s.links.Headlamp("Kustomization", "flux-system", k)}
		w.flux.Kustomizations = append(w.flux.Kustomizations, ks)
	}
	for _, hr := range []struct{ ns, name, ver string }{
		{"kube-system", "cilium", "1.18.2"}, {"cert-manager", "cert-manager", "v1.19.1"},
		{"monitoring", "victoria-metrics", "0.62.0"}, {"monitoring", "grafana", "10.1.2"},
		{"cnpg-system", "cloudnative-pg", "0.26.0"}, {"headlamp", "headlamp", "0.36.0"},
		{"external-secrets", "external-secrets", "0.20.3"},
	} {
		w.flux.HelmReleases = append(w.flux.HelmReleases, model.FluxObj{
			Kind: model.FluxKindHelmRelease, Namespace: hr.ns, Name: hr.name, Ready: true, Revision: hr.ver,
			Message:        "Helm upgrade succeeded for release " + hr.ns + "/" + hr.name + " with chart " + hr.name + "@" + hr.ver,
			LastTransition: agoP(time.Duration(len(hr.name)) * 3 * time.Hour), Links: s.links.Headlamp("HelmRelease", hr.ns, hr.name),
		})
	}
	suspended := model.FluxObj{Kind: model.FluxKindHelmRelease, Namespace: "acme", Name: "acme-preview", Suspended: true,
		SuspendReason: "paused while the preview env is rebuilt", Ready: true, Revision: "1.4.0", Message: "suspended",
		LastTransition: agoP(2 * 24 * time.Hour), Links: s.links.Headlamp("HelmRelease", "acme", "acme-preview")}
	w.flux.HelmReleases = append(w.flux.HelmReleases, suspended)

	// ---- storage ----
	type pvcSpec struct {
		ns, name, sc string
		capGiB, used float64
		growth       float64
	}
	for _, p := range []pvcSpec{
		{"monitoring", "vmsingle-data", "local-zfs", 50, 0.83, 0.6},
		{"monitoring", "grafana", "local-zfs", 5, 0.21, 0.01},
		{"registry", "zot-data", "local-zfs", 100, 0.46, 0.8},
		{"acme", "acme-db-1", "local-zfs", 20, 0.38, 0.05},
		{"acme", "acme-db-2", "local-zfs", 20, 0.38, 0.05},
		{"dash", "dash-cache", "local-zfs", 1, 0.09, 0},
		{"gatus", "gatus-data", "local-zfs", 2, 0.27, 0.01},
	} {
		capB := gib(p.capGiB)
		ratio := around(p.name, now, p.used, 0.004)
		used := int64(float64(capB) * ratio)
		h := model.HealthOK
		if ratio >= 0.8 {
			h = model.HealthWarn
		}
		w.storage.PVCs = append(w.storage.PVCs, model.PVC{
			Namespace: p.ns, Name: p.name, StorageClass: p.sc, Phase: "Bound", CapacityBytes: capB,
			UsedBytes: i64(used), UsedRatio: f64(ratio), GrowthBytesPerDay: f64(p.growth * (1 << 30)),
			Health: h, Links: s.links.Headlamp("PersistentVolumeClaim", p.ns, p.name),
		})
	}
	w.storage.Buckets = []model.Bucket{
		{Name: "cnpg-backups", UsedBytes: gib(38), QuotaBytes: gib(200), Health: model.HealthOK},
		{Name: "velero", UsedBytes: gib(112), QuotaBytes: gib(500), Health: model.HealthOK},
		{Name: "media-cache", UsedBytes: gib(9), Health: model.HealthOK},
	}
	w.storage.RustFS = model.UsedSize{UsedBytes: gib(159), UsableBytes: gib(1800)}
	w.storage.PVEStorages = []model.PVEStorage{
		{Name: "local", UsedBytes: gib(18), SizeBytes: gib(94)},
		{Name: "local-zfs", UsedBytes: gib(612), SizeBytes: gib(1700)},
		{Name: "tank", UsedBytes: gib(4100), SizeBytes: gib(10900)},
	}
	w.storage.CNPG = []model.CNPGCluster{{Namespace: "acme", Name: "acme-db", Instances: 2, ReadyInstances: 2,
		Phase: "Cluster in healthy state", LastBackupAgeSeconds: f64(around("cnpgbk", now, 5.2*3600, 1800)),
		Links: s.links.Headlamp("Cluster", "acme", "acme-db")}}
	w.storage.NASBackups = []model.NASBackup{{Name: "nas-backup", LastSuccessAgeSeconds: f64(7.5 * 3600)}}
	w.storage.Etcd = model.Etcd{DBBytes: 92 << 20, QuotaBytes: 2 << 30}

	// ---- alerts ----
	w.alerts.Alerts = []model.Alert{
		{Fingerprint: "a1b2c3d4e5f60001", Name: "KubePodCrashLooping", Severity: "warning", State: "active",
			Summary:     "Pod acme/" + crashPod + " is crash looping",
			Description: "Container app has restarted 14 times in the last hour.",
			Labels:      map[string]string{"alertname": "KubePodCrashLooping", "namespace": "acme", "pod": crashPod, "severity": "warning"},
			StartsAt:    ago(41 * time.Minute), SilencedBy: []string{}, InhibitedBy: []string{}},
		{Fingerprint: "a1b2c3d4e5f60002", Name: "PersistentVolumeFillingUp", Severity: "warning", State: "active",
			Summary:     "PVC monitoring/vmsingle-data is 83% full",
			Description: "At the current rate it fills up in about 14 days.",
			Labels:      map[string]string{"alertname": "PersistentVolumeFillingUp", "namespace": "monitoring", "persistentvolumeclaim": "vmsingle-data", "severity": "warning"},
			StartsAt:    ago(3 * time.Hour), SilencedBy: []string{}, InhibitedBy: []string{}},
		{Fingerprint: "a1b2c3d4e5f60003", Name: "Watchdog", Severity: "none", State: "suppressed",
			Summary: "Alerting pipeline is alive", Description: "Always firing.",
			Labels:   map[string]string{"alertname": "Watchdog", "severity": "none"},
			StartsAt: ago(38 * 24 * time.Hour), SilencedBy: []string{"6f1e3d2c-demo"}, InhibitedBy: []string{}},
	}
	w.alerts.Silences = []model.Silence{{ID: "6f1e3d2c-demo", Matchers: []model.Matcher{{Name: "alertname", Value: "Watchdog", IsEqual: true}},
		CreatedBy: "demo", Comment: "heartbeat goes to the dead-man switch, not the UI",
		StartsAt: ago(2 * 24 * time.Hour), EndsAt: ago(-5 * 24 * time.Hour), State: "active"}}
	firing := 0
	for _, a := range w.alerts.Alerts {
		if a.State == "active" {
			firing++
		}
	}

	// ---- status ----
	type epSpec struct {
		group, name string
		ms          float64
	}
	for _, e := range []epSpec{
		{"platform", "dash", 42}, {"platform", "grafana", 88}, {"platform", "headlamp", 61},
		{"platform", "registry", 35}, {"apps", "homepage", 54}, {"apps", "gatus", 12},
		{"tenants", "acme-web", 120}, {"tenants", "acme-api", 74}, {"edge", "public-site", 210},
	} {
		ep := model.Endpoint{Group: e.group, Name: e.name, Key: e.group + "_" + e.name,
			URL: "https://" + e.name + ".example.com", Healthy: true}
		for i := 39; i >= 0; i-- {
			t := now.Add(-time.Duration(i) * time.Minute).Truncate(time.Minute)
			ok := !(e.name == "acme-api" && (i == 31 || i == 30))
			ep.Results = append(ep.Results, model.CheckResult{Success: ok, Timestamp: t.UTC(),
				DurationMs: around(e.name+t.String(), t, e.ms, e.ms*0.3)})
		}
		w.status.Endpoints = append(w.status.Endpoints, ep)
	}

	// ---- apps ----
	icon := func(n string) string {
		return "https://cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons/png/" + n + ".png"
	}
	for _, a := range []struct{ name, desc, icon, ns string }{
		{"Grafana", "Dashboards and metrics", "grafana", "monitoring"},
		{"Headlamp", "Kubernetes UI", "headlamp", "headlamp"},
		{"Homepage", "Start page", "homepage", "homepage"},
		{"Gatus", "Service status", "gatus", "gatus"},
		{"Zot", "Container registry", "zot-registry", "registry"},
		{"Acme web", "Tenant storefront", "nginx", "acme"},
	} {
		g := model.GroupApps
		if a.ns == "monitoring" {
			g = model.GroupPlatform
		}
		if a.ns == "acme" {
			g = model.GroupTenants
		}
		w.apps.Apps = append(w.apps.Apps, model.App{Name: a.name, Description: a.desc, IconURL: icon(a.icon),
			Href: "https://" + strings.ToLower(strings.ReplaceAll(a.name, " ", "-")) + ".example.com", Group: g, Namespace: a.ns, Healthy: &yes})
	}

	// ---- overview ----
	worst := w.storage.PVCs[0]
	w.overview.Attention = []model.AttentionItem{
		{ID: "pod/" + crashingNS + "/" + crashPod, Severity: model.SeverityCrit, Source: "kubernetes",
			Title: "acme/" + crashPod + " is in CrashLoopBackOff", Detail: "14 restarts · last exit: Error (1)",
			Since: ago(47 * time.Minute), Route: "/cluster/pods/" + crashingNS + "/" + crashPod,
			Links: s.links.Headlamp("Pod", crashingNS, crashPod)},
		{ID: "pvc/monitoring/vmsingle-data", Severity: model.SeverityWarn, Source: "storage",
			Title: "monitoring/vmsingle-data is 83% full", Detail: "growing ~0.6 GiB/day · full in ~14 days",
			Since: ago(3 * time.Hour), Route: "/storage", Links: worst.Links},
	}
	nsHealth := map[string]model.Health{}
	for _, n := range w.cluster.Namespaces {
		nsHealth[n.Name] = n.Health
	}
	for _, g := range []model.Group{model.GroupPlatform, model.GroupApps, model.GroupTenants} {
		tg := model.TopologyGroup{Name: g, Health: model.HealthOK, PodsByNode: groupPods[g]}
		for _, n := range w.cluster.Namespaces {
			if n.Group != g {
				continue
			}
			tg.Namespaces = append(tg.Namespaces, model.TopologyNamespace{Name: n.Name, Health: n.Health, Pods: n.PodsTotal})
			if n.Health == model.HealthCrit {
				tg.Health = model.HealthCrit
			}
		}
		w.overview.Topology.Groups = append(w.overview.Topology.Groups, tg)
	}
	w.overview.Topology.Host = model.TopologyHost{Name: hostName, Health: model.HealthOK, PowerWatts: power,
		CPUTempC: max(temps[0], temps[1]), MemUsedRatio: float64(memUsed) / float64(memTotal)}
	running, total := 0, 0
	for _, vm := range w.vms.VMs {
		if vm.Template {
			continue
		}
		total++
		if vm.Status == "running" {
			running++
		}
		w.overview.Topology.VMs = append(w.overview.Topology.VMs, model.TopologyVM{VMID: vm.VMID, Name: vm.Name,
			Health: vm.Health, CPURatio: vm.CPURatio, MemRatio: float64(vm.MemBytes) / float64(vm.MaxMemBytes),
			IsK8sNode: vm.K8sNode != nil, Running: vm.Status == "running"})
	}
	fluxTotal := len(w.flux.Sources) + len(w.flux.Kustomizations) + len(w.flux.HelmReleases)
	w.overview.Status = model.HealthCrit
	w.overview.Kpis = model.Kpis{
		AlertsFiring: firing, FluxReady: fluxTotal, FluxTotal: fluxTotal, FluxSuspended: 1, SourceVerified: true,
		NodesReady: len(nodeNames), NodesTotal: len(nodeNames), VMsRunning: running, VMsTotal: total,
		PowerWatts: power, CPUTempMaxC: max(temps[0], temps[1]),
		WorstPVC: &model.WorstPVC{Namespace: worst.Namespace, Name: worst.Name, UsedRatio: *worst.UsedRatio},
	}
	return w
}
