package overview

import (
	"cmp"
	"slices"
	"sort"
	"strings"

	"github.com/TechDufus/dash/internal/model"
	"github.com/TechDufus/dash/internal/plugin"
)

// contribution is what one source adds to the overview. Items and KPIs of a contribution with a
// plugin carry that plugin's ID; one without leaves them as they are.
type contribution struct {
	plugin    plugin.ID
	attention []model.AttentionItem
	kpis      []model.KPI
	topology  *model.Topology
}

// compose merges contributions into the overview.
//
//   - Attention is concatenated and ordered critical first, then newest first, then by ID; an item
//     of a contributing plugin carries that plugin's ID.
//   - Status is the worst severity of the items.
//   - KPIList is every contributed KPI, ordered by plugin and key.
//   - The topology takes the host of the first contribution that names one, appends the VMs, and
//     merges groups by name.
func compose(cs []contribution) model.OverviewData {
	items := []model.AttentionItem{}
	kpiList := []model.KPI{}
	var topologies []model.Topology
	for _, c := range cs {
		for _, it := range c.attention {
			if c.plugin != "" {
				it.Plugin = string(c.plugin)
			}
			if it.Links == nil {
				it.Links = []model.Link{}
			}
			items = append(items, it)
		}
		for _, k := range c.kpis {
			if c.plugin != "" {
				k.Plugin = string(c.plugin)
			}
			kpiList = append(kpiList, k)
		}
		if c.topology != nil {
			topologies = append(topologies, *c.topology)
		}
	}
	sortAttention(items)
	sort.SliceStable(kpiList, func(i, j int) bool {
		if kpiList[i].Plugin != kpiList[j].Plugin {
			return kpiList[i].Plugin < kpiList[j].Plugin
		}
		return kpiList[i].Key < kpiList[j].Key
	})
	return model.OverviewData{
		Status:    OverallStatus(items),
		Attention: items,
		KPIList:   kpiList,
		Topology:  mergeTopology(topologies),
	}
}

// mergeSources appends extra to base without repeating a name: for a name in both, the failing
// entry wins. Base order is kept, and new names follow in extra's order.
func mergeSources(base, extra []model.SourceStatus) []model.SourceStatus {
	out := slices.Clone(base)
	if out == nil {
		out = []model.SourceStatus{}
	}
	index := make(map[string]int, len(out))
	for i, s := range out {
		index[s.Name] = i
	}
	for _, s := range extra {
		i, dup := index[s.Name]
		switch {
		case !dup:
			index[s.Name] = len(out)
			out = append(out, s)
		case !s.OK && out[i].OK:
			out[i] = s
		}
	}
	return out
}

// mergeTopology merges the topologies the contributions describe, in contribution order. The hosts
// several contributions name for one node are one host (mergeHosts); the VMs are appended; the
// groups are merged by name.
func mergeTopology(ts []model.Topology) model.Topology {
	out := model.Topology{Hosts: mergeHosts(ts), VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{}}
	index := map[model.Group]int{}
	for _, t := range ts {
		out.VMs = append(out.VMs, t.VMs...)
		for _, g := range t.Groups {
			i, seen := index[g.Name]
			if !seen {
				index[g.Name] = len(out.Groups)
				out.Groups = append(out.Groups, model.TopologyGroup{
					Name: g.Name, Health: g.Health, Namespaces: slices.Clone(g.Namespaces), PodsByNode: map[string]int{},
				})
				i = index[g.Name]
				if out.Groups[i].Namespaces == nil {
					out.Groups[i].Namespaces = []model.TopologyNamespace{}
				}
			} else {
				out.Groups[i].Health = worst(out.Groups[i].Health, g.Health)
				out.Groups[i].Namespaces = append(out.Groups[i].Namespaces, g.Namespaces...)
			}
			for node, n := range g.PodsByNode {
				out.Groups[i].PodsByNode[node] += n
			}
		}
	}
	for i := range out.Groups {
		slices.SortStableFunc(out.Groups[i].Namespaces, func(a, b model.TopologyNamespace) int { return strings.Compare(a.Name, b.Name) })
	}
	return out
}

// mergeHosts is the hosts of every contribution, sorted by name and never nil. The hosts with one
// Node are one host, merged in contribution order: each field takes the first non-zero value and
// the health the worst. Its name is one that differs from the node when any contribution gives one
// (an operator's display name, e.g. hardware's hosts[].name), else the node. A host without a node
// matches nothing and stays as it comes.
func mergeHosts(ts []model.Topology) []model.TopologyHost {
	out := []model.TopologyHost{}
	byNode := map[string]int{}
	for _, t := range ts {
		for _, h := range t.Hosts {
			if h.Node == "" {
				out = append(out, h)
				continue
			}
			i, seen := byNode[h.Node]
			if !seen {
				byNode[h.Node] = len(out)
				out = append(out, h)
				continue
			}
			m := &out[i]
			if m.Name == "" || (m.Name == m.Node && h.Name != "" && h.Name != h.Node) {
				m.Name = h.Name
			}
			m.Health = worst(m.Health, h.Health)
			m.PowerWatts = cmp.Or(m.PowerWatts, h.PowerWatts)
			m.CPUTempC = cmp.Or(m.CPUTempC, h.CPUTempC)
			m.CPUTempHealth = cmp.Or(m.CPUTempHealth, h.CPUTempHealth)
			m.MemUsedRatio = cmp.Or(m.MemUsedRatio, h.MemUsedRatio)
		}
	}
	for i := range out {
		if out[i].Name == "" {
			out[i].Name = out[i].Node
		}
		// A host only Proxmox describes has no temperature reading: its health is unknown, not an
		// empty value outside the Health enum.
		if out[i].CPUTempHealth == "" {
			out[i].CPUTempHealth = model.HealthUnknown
		}
	}
	slices.SortStableFunc(out, func(a, b model.TopologyHost) int { return strings.Compare(a.Name, b.Name) })
	return out
}
