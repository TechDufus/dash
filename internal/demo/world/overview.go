package world

import (
	"fmt"
	"slices"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// Overview is the summary payload. It builds the topics it summarises (cluster, host, VMs, flux,
// storage, alerts) at the same instant, so the tiles agree with the pages behind them.
func (w *World) Overview() model.OverviewData {
	now := w.clock()
	return w.overviewOf(now, Snapshot{
		Host:    w.host(now),
		VMs:     w.vms(now),
		Cluster: w.cluster(now),
		Flux:    w.flux(now),
		Storage: w.storage(now),
		Alerts:  w.alerts(now),
	})
}

// overviewOf derives the overview from the topic payloads of s (Overview, Status and Apps are not
// read).
func (w *World) overviewOf(now time.Time, s Snapshot) model.OverviewData {
	var o model.OverviewData
	crashPod := crashPodName()
	worst := s.Storage.PVCs[0]
	o.Attention = []model.AttentionItem{
		{ID: "pod/" + crashingNS + "/" + crashPod, Severity: model.SeverityCrit, Source: "kubernetes", Plugin: "kubernetes",
			Title: "acme/" + crashPod + " is in CrashLoopBackOff", Detail: "14 restarts · last exit: Error (1)",
			Since: ago(now, 47*time.Minute), Route: "/cluster/pods/" + crashingNS + "/" + crashPod,
			Links: w.links.Headlamp("Pod", crashingNS, crashPod)},
		{ID: "pvc/monitoring/vmsingle-data", Severity: model.SeverityWarn, Source: "storage", Plugin: "kubernetes",
			Title: "monitoring/vmsingle-data is 83% full", Detail: "growing ~0.6 GiB/day · full in ~14 days",
			Since: ago(now, 3*time.Hour), Route: "/storage", Links: worst.Links},
	}
	nsGroup := map[string]model.Group{}
	for _, n := range s.Cluster.Namespaces {
		nsGroup[n.Name] = n.Group
	}
	groupPods := map[model.Group]map[string]int{}
	for _, p := range s.Cluster.Pods {
		g := nsGroup[p.Namespace]
		if groupPods[g] == nil {
			groupPods[g] = map[string]int{}
		}
		groupPods[g][p.Node]++
	}
	for _, g := range []model.Group{model.GroupPlatform, model.GroupApps, model.GroupTenants} {
		tg := model.TopologyGroup{Name: g, Health: model.HealthOK, PodsByNode: groupPods[g]}
		for _, n := range s.Cluster.Namespaces {
			if n.Group != g {
				continue
			}
			tg.Namespaces = append(tg.Namespaces, model.TopologyNamespace{Name: n.Name, Health: n.Health, Pods: n.PodsTotal})
			if n.Health == model.HealthCrit {
				tg.Health = model.HealthCrit
			}
		}
		o.Topology.Groups = append(o.Topology.Groups, tg)
	}
	temps := s.Host.PackageTempsC
	cpuTemp := max(temps[0], temps[1])
	o.Topology.Host = model.TopologyHost{Name: HostName, Health: model.HealthOK, PowerWatts: s.Host.PowerWatts,
		CPUTempC: cpuTemp, CPUTempHealth: model.HealthOK, MemUsedRatio: float64(s.Host.MemUsedBytes) / float64(s.Host.MemTotalBytes)}
	running, total := 0, 0
	for _, vm := range s.VMs.VMs {
		if vm.Template {
			continue
		}
		total++
		if vm.Status == "running" {
			running++
		}
		o.Topology.VMs = append(o.Topology.VMs, model.TopologyVM{VMID: vm.VMID, Name: vm.Name,
			Health: vm.Health, CPURatio: vm.CPURatio, MemRatio: float64(vm.MemBytes) / float64(vm.MaxMemBytes),
			IsK8sNode: vm.K8sNode != nil, Running: vm.Status == "running"})
	}
	firing, alertHealth := 0, model.HealthOK
	for _, a := range s.Alerts.Alerts {
		if a.State == "active" {
			firing++
			if alertHealth == model.HealthOK {
				alertHealth = model.HealthWarn
			}
			if a.Severity == "critical" {
				alertHealth = model.HealthCrit
			}
		}
	}
	o.Status = model.HealthCrit
	// The alertmanager plugin's one KPI, as its KPIs() computes it for the default severityMap, the
	// proxmox plugin's two, as its kpisOf computes them, and the hardware plugin's two (ordered by
	// plugin, as compose does).
	vmHealth := model.HealthOK
	if running < total {
		vmHealth = model.HealthWarn
	}
	// The kubernetes plugin's three, as its kpisOf computes them: nodes ready, nodes, and the
	// fullest PVC that reports a usage ratio.
	nodesReady := 0
	for _, n := range s.Cluster.Nodes {
		if n.Ready {
			nodesReady++
		}
	}
	nodesHealth := model.HealthOK
	if nodesReady < len(s.Cluster.Nodes) {
		nodesHealth = model.HealthCrit
	}
	var fullest *model.PVC
	for i, p := range s.Storage.PVCs {
		if p.UsedRatio != nil && (fullest == nil || *p.UsedRatio > *fullest.UsedRatio) {
			fullest = &s.Storage.PVCs[i]
		}
	}
	pvcHealth := model.HealthOK
	if r := *fullest.UsedRatio; r >= 0.90 {
		pvcHealth = model.HealthCrit
	} else if r >= 0.80 {
		pvcHealth = model.HealthWarn
	}
	fluxKPIs, fluxItems := fluxOverview(s.Flux)
	o.Attention = append(o.Attention, fluxItems...)
	o.KPIList = []model.KPI{
		{Key: "firing", Plugin: "alertmanager", Label: "Alerts firing", Value: float64(firing), Health: alertHealth},
		{Key: "power", Plugin: "hardware", Label: "Power", Unit: "W", Value: s.Host.PowerWatts, Health: model.HealthOK},
		{Key: "cpuTemp", Plugin: "hardware", Label: "CPU temp", Unit: "°C", Value: cpuTemp, Health: model.HealthOK},
		{Key: "nodes.ready", Plugin: "kubernetes", Label: "Nodes ready", Value: float64(nodesReady), Health: nodesHealth},
		{Key: "nodes.total", Plugin: "kubernetes", Label: "Nodes", Value: float64(len(s.Cluster.Nodes)), Health: model.HealthOK},
		{Key: "pvc.worst", Plugin: "kubernetes", Label: fullest.Namespace + "/" + fullest.Name, Value: *fullest.UsedRatio, Health: pvcHealth},
		{Key: "vms.running", Plugin: "proxmox", Label: "VMs running", Value: float64(running), Health: vmHealth},
		{Key: "vms.total", Plugin: "proxmox", Label: "VMs", Value: float64(total), Health: model.HealthOK},
	}
	o.KPIList = slices.Insert(o.KPIList, 1, fluxKPIs...) // after alertmanager, before hardware: by plugin, as compose does
	return o
}

// fluxOverview is the flux plugin's contribution to the overview, as its KPIs() and Attention()
// compute it for the fake lab (whose GitOps repository is pinned, FluxPinned): the Kustomizations and
// Helm releases ready, suspended and in all, whether the signed sources are verified, and one
// warning per suspended object.
func fluxOverview(d model.FluxData) ([]model.KPI, []model.AttentionItem) {
	ready, total, suspended := 0, 0, 0
	for _, list := range [][]model.FluxObj{d.Kustomizations, d.HelmReleases} {
		for _, o := range list {
			total++
			if o.Ready {
				ready++
			}
			if o.Suspended {
				suspended++
			}
		}
	}
	items := []model.AttentionItem{}
	for _, list := range [][]model.FluxObj{d.Sources, d.Kustomizations, d.HelmReleases} {
		for _, o := range list {
			if !o.Suspended {
				continue
			}
			detail := o.SuspendReason
			if detail == "" {
				detail = "suspended"
			}
			var since time.Time
			if o.LastTransition != nil {
				since = *o.LastTransition
			}
			items = append(items, model.AttentionItem{
				ID:       fmt.Sprintf("flux:suspended/%s/%s/%s", o.Kind, o.Namespace, o.Name),
				Severity: model.SeverityWarn, Source: "flux", Plugin: "flux", Detail: detail, Since: since, Route: "/gitops", Links: o.Links,
				Title: fmt.Sprintf("%s %s/%s suspended", o.Kind, o.Namespace, o.Name), Ref: plugin.Ref("flux", string(o.Kind), o.Namespace, o.Name),
			})
		}
	}
	readyHealth := model.HealthOK
	if ready < total {
		readyHealth = model.HealthWarn
	}
	verified, verifiedHealth := 1, model.HealthOK
	for _, s := range d.Sources {
		if s.SpecVerify && (s.Verified == nil || !*s.Verified) {
			verified, verifiedHealth = 0, model.HealthCrit
		}
	}
	return []model.KPI{
		{Key: "ready", Plugin: "flux", Label: "Flux ready", Value: float64(ready), Health: readyHealth},
		{Key: "suspended", Plugin: "flux", Label: "Flux suspended", Value: float64(suspended), Health: model.HealthOK},
		{Key: "total", Plugin: "flux", Label: "Flux objects", Value: float64(total), Health: model.HealthOK},
		{Key: "verified", Plugin: "flux", Label: "Source verified", Value: float64(verified), Health: verifiedHealth},
	}, items
}
