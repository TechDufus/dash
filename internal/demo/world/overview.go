package world

import (
	"time"

	"github.com/TechDufus/dash/internal/model"
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
		CPUTempC: cpuTemp, MemUsedRatio: float64(s.Host.MemUsedBytes) / float64(s.Host.MemTotalBytes)}
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
	fluxTotal := len(s.Flux.Sources) + len(s.Flux.Kustomizations) + len(s.Flux.HelmReleases)
	o.Status = model.HealthCrit
	// The alertmanager plugin's one KPI, as its KPIs() computes it for the default severityMap.
	o.KPIList = []model.KPI{{Key: "firing", Plugin: "alertmanager", Label: "Alerts firing", Value: float64(firing), Health: alertHealth}}
	o.Kpis = model.Kpis{
		FluxReady: fluxTotal, FluxTotal: fluxTotal, FluxSuspended: 1, SourceVerified: true,
		NodesReady: len(nodeNames), NodesTotal: len(nodeNames), VMsRunning: running, VMsTotal: total,
		PowerWatts: s.Host.PowerWatts, CPUTempMaxC: cpuTemp,
		WorstPVC: &model.WorstPVC{Namespace: worst.Namespace, Name: worst.Name, UsedRatio: *worst.UsedRatio},
	}
	return o
}
