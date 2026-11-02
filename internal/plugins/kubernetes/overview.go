package kubernetes

import (
	"fmt"
	"sort"
	"time"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugins/internal/health"
)

// The keys of the plugin's KPIs.
const (
	kpiNodesReady = "nodes.ready"
	kpiNodesTotal = "nodes.total"
	kpiWorstPVC   = "pvc.worst"
)

// Attention implements plugin.AttentionSource: the rule table over the payloads the plugin
// published last. It has no opinion (nil) until both the cluster and the storage payload exist,
// so a restarted plugin does not prune the supervisor's first sightings with a half-built list;
// an item without a time of its own gets the time it was first seen, from the supervisor.
func (p *Plugin) Attention(now time.Time) []model.AttentionItem {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	cl, st := r.builtPayloads()
	if cl == nil || st == nil {
		return nil
	}
	return attentionOf(*cl, *st, now)
}

// KPIs implements plugin.KPISource: the nodes that are ready and the nodes there are, once the
// cluster payload exists, and the fullest PVC that reports a usage ratio. The ready tile is
// critical while a node is not ready; the PVC follows the storage thresholds.
func (p *Plugin) KPIs() []model.KPI {
	r := p.rt.Load()
	if r == nil {
		return nil
	}
	cl, st := r.builtPayloads()
	return kpisOf(cl, st)
}

// kpisOf derives the KPIs from the published payloads; either may be nil before its first
// publish and then contributes nothing.
func kpisOf(cl *model.ClusterData, st *model.StorageData) []model.KPI {
	out := []model.KPI{}
	if cl != nil {
		ready := 0
		for _, n := range cl.Nodes {
			if n.Ready {
				ready++
			}
		}
		h := model.HealthOK
		if ready < len(cl.Nodes) {
			h = model.HealthCrit
		}
		out = append(out,
			model.KPI{Key: kpiNodesReady, Plugin: string(ID), Label: "Nodes ready", Value: float64(ready), Health: h},
			model.KPI{Key: kpiNodesTotal, Plugin: string(ID), Label: "Nodes", Value: float64(len(cl.Nodes)), Health: model.HealthOK},
		)
	}
	if st != nil {
		var worstPVC *model.PVC
		for i, p := range st.PVCs {
			if p.UsedRatio != nil && (worstPVC == nil || *p.UsedRatio > *worstPVC.UsedRatio) {
				worstPVC = &st.PVCs[i]
			}
		}
		if worstPVC != nil {
			out = append(out, model.KPI{
				Key: kpiWorstPVC, Plugin: string(ID), Label: fmt.Sprintf("%s/%s", worstPVC.Namespace, worstPVC.Name),
				Value: *worstPVC.UsedRatio, Health: health.RatioHealth(*worstPVC.UsedRatio),
			})
		}
	}
	return out
}

// Topology implements plugin.TopologySource: the cluster's three groups with their namespaces and
// the pods they run on each node. It has no host and no VMs of its own.
func (p *Plugin) Topology() model.Topology {
	r := p.rt.Load()
	if r == nil {
		return model.Topology{VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{}}
	}
	cl, _ := r.builtPayloads()
	var c model.ClusterData
	if cl != nil {
		c = *cl
	}
	return topologyOf(c)
}

// topologyOf is the Platform, Apps and Tenants groups of the cluster payload.
func topologyOf(cl model.ClusterData) model.Topology {
	t := model.Topology{VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{}}
	order := []model.Group{model.GroupPlatform, model.GroupApps, model.GroupTenants}

	groupOf := map[string]model.Group{}
	for _, ns := range cl.Namespaces {
		groupOf[ns.Name] = ns.Group
	}
	groups := map[model.Group]*model.TopologyGroup{}
	for _, g := range order {
		groups[g] = &model.TopologyGroup{Name: g, Health: model.HealthOK, Namespaces: []model.TopologyNamespace{}, PodsByNode: map[string]int{}}
	}
	for _, ns := range cl.Namespaces {
		g := groups[ns.Group]
		if g == nil {
			continue
		}
		g.Namespaces = append(g.Namespaces, model.TopologyNamespace{Name: ns.Name, Health: ns.Health, Pods: ns.PodsTotal})
		g.Health = worst(g.Health, ns.Health)
	}
	for _, g := range groups {
		sort.Slice(g.Namespaces, func(i, j int) bool { return g.Namespaces[i].Name < g.Namespaces[j].Name })
	}
	for _, p := range cl.Pods {
		if p.Node == "" || p.Phase == "Succeeded" || p.Phase == "Failed" {
			continue
		}
		if g, ok := groups[groupOf[p.Namespace]]; ok {
			g.PodsByNode[p.Node]++
		}
	}
	for _, g := range order {
		t.Groups = append(t.Groups, *groups[g])
	}
	return t
}
