package topics

import (
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

// mergeTopology merges the topologies the contributions describe, in contribution order.
func mergeTopology(ts []model.Topology) model.Topology {
	out := model.Topology{VMs: []model.TopologyVM{}, Groups: []model.TopologyGroup{}}
	if len(ts) > 0 {
		out.Host = ts[0].Host
	}
	for _, t := range ts {
		if t.Host.Name != "" {
			out.Host = t.Host
			break
		}
	}
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
				out.Groups[i].Health = Worst(out.Groups[i].Health, g.Health)
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
